// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// welcomeStubLLM 记录调用次数并返回固定结果，用于验证冷却与回落行为。
type welcomeStubLLM struct {
	mu    sync.Mutex
	calls int
	text  string
	err   error
	last  llm.GenerateRequest
}

func (s *welcomeStubLLM) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.last = req
	if s.err != nil {
		return nil, s.err
	}
	return &llm.GenerateResponse{Text: s.text}, nil
}

func (s *welcomeStubLLM) lastRequest() llm.GenerateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *welcomeStubLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func welcomeNoticeEvent(userID string) MessageEvent {
	return MessageEvent{Kind: EventKindNotice, SubType: "group_increase", GroupID: "g-welcome", UserID: userID}
}

func newWelcomeRuntime(cfg BotConfig, channel *recordingChannel, store LLMProfileStore) *Runtime {
	return NewRuntime(cfg, channel, NewPluginManager(), store, nil, nil, nil)
}

func TestWelcomeTemplateModePicksFromPool(t *testing.T) {
	channel := &recordingChannel{}
	pool := []string{"哟，{user_id} 来了", "欢迎 {user_id} 入群～"}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled:   true,
		WelcomeMode:      WelcomeModeTemplate,
		WelcomeMessage:   "固定欢迎 {user_id}",
		WelcomeTemplates: pool,
	}, channel, nil)

	for _, userID := range []string{"u1", "u2", "u3"} {
		if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent(userID)); err != nil {
			t.Fatal(err)
		}
	}
	if len(channel.sent) != 3 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	allowed := map[string]bool{}
	for _, template := range pool {
		allowed[strings.ReplaceAll(template, "{user_id}", "")] = true
	}
	for i, msg := range channel.sent {
		if msg.MentionUserID == "" {
			t.Fatalf("msg %d missing mention: %#v", i, msg)
		}
		if !allowed[strings.ReplaceAll(msg.Text, msg.MentionUserID, "")] {
			t.Fatalf("msg %d not from template pool: %q", i, msg.Text)
		}
	}
}

func TestWelcomeTemplateModeFallsBackToFixedWhenPoolEmpty(t *testing.T) {
	channel := &recordingChannel{}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled: true,
		WelcomeMode:    WelcomeModeTemplate,
		WelcomeMessage: "固定欢迎 {user_id}",
	}, channel, nil)

	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "固定欢迎 u1" {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

func TestWelcomeLLMModeGeneratesAndCooldownFallsBack(t *testing.T) {
	channel := &recordingChannel{}
	stub := &welcomeStubLLM{text: "  「欢迎新朋友！」\n\n"}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled:            true,
		WelcomeMode:               WelcomeModeLLM,
		WelcomeMessage:            "固定欢迎 {user_id}",
		WelcomeTemplates:          []string{"模板欢迎 {user_id}"},
		WelcomeLLMCooldownSeconds: 300,
		SystemPrompt:              "一只高冷的猫娘",
	}, channel, &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "k", BaseURL: "http://localhost/v1", Model: "m",
	})})
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return stub, nil })

	// 第一次：LLM 成功，输出经清洗（去引号、压缩空白）。
	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "欢迎新朋友！" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if stub.callCount() != 1 {
		t.Fatalf("llm calls = %d", stub.callCount())
	}

	// 冷却期内第二次：回落模板池，不再调用 LLM。
	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u2")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 2 || channel.sent[1].Text != "模板欢迎 u2" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if stub.callCount() != 1 {
		t.Fatalf("llm calls after cooldown = %d", stub.callCount())
	}
}

func TestWelcomeLLMModeFallsBackWhenLLMFails(t *testing.T) {
	channel := &recordingChannel{}
	stub := &welcomeStubLLM{err: context.DeadlineExceeded}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled:            true,
		WelcomeMode:               WelcomeModeLLM,
		WelcomeMessage:            "固定欢迎 {user_id}",
		WelcomeLLMCooldownSeconds: 300,
	}, channel, &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "k", BaseURL: "http://localhost/v1", Model: "m",
	})})
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return stub, nil })

	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "固定欢迎 u1" {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

func TestWelcomeLLMModeFallsBackOnGarbageOutput(t *testing.T) {
	channel := &recordingChannel{}
	stub := &welcomeStubLLM{text: "  \"\"  "}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled:            true,
		WelcomeMode:               WelcomeModeLLM,
		WelcomeMessage:            "固定欢迎 {user_id}",
		WelcomeLLMCooldownSeconds: 300,
	}, channel, &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "k", BaseURL: "http://localhost/v1", Model: "m",
	})})
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return stub, nil })

	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "固定欢迎 u1" {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

func TestWelcomeGroupConfigOverridesModeAndPool(t *testing.T) {
	channel := &recordingChannel{}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled: true,
		WelcomeMode:    WelcomeModeFixed,
		WelcomeMessage: "全局固定 {user_id}",
	}, channel, nil)
	runtime.SetGroupConfigStore(staticGroupConfigStore{cfg: GroupConfig{
		GroupID: "g-welcome", Enabled: true, EnabledSet: true,
		WelcomeEnabled:   boolPointer(true),
		WelcomeMode:      WelcomeModeTemplate,
		WelcomeTemplates: []string{"本群模板 {user_id}"},
	}})

	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "本群模板 u1" {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

func TestWelcomeModeDefaultsToFixed(t *testing.T) {
	cfg := BotConfig{WelcomeEnabled: true, WelcomeMessage: "欢迎"}.WithDefaults()
	if cfg.WelcomeMode != WelcomeModeFixed {
		t.Fatalf("welcome mode = %q", cfg.WelcomeMode)
	}
	if cfg.WelcomeLLMCooldownSeconds != defaultWelcomeLLMCooldownSeconds {
		t.Fatalf("cooldown = %d", cfg.WelcomeLLMCooldownSeconds)
	}
	if got := normalizeWelcomeMode("bogus"); got != WelcomeModeFixed {
		t.Fatalf("normalize bogus = %q", got)
	}
}

func newLLMWelcomeRuntime(cfg BotConfig, channel *recordingChannel, stub *welcomeStubLLM) *Runtime {
	runtime := newWelcomeRuntime(cfg, channel, &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "k", BaseURL: "http://localhost/v1", Model: "m",
	})})
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return stub, nil })
	return runtime
}

func welcomeRequestDump(req llm.GenerateRequest) string {
	var builder strings.Builder
	for _, message := range req.Messages {
		builder.WriteString(string(message.Role) + ": " + message.Content + "\n")
		for _, part := range message.Parts {
			builder.WriteString(part.Text + "\n")
		}
	}
	return builder.String()
}

// 欢迎和普通回复一样：人设、最近群聊、新人昵称、群名都进请求，最近说过的欢迎也带上。
func TestWelcomeLLMModeUsesNormalReplyContext(t *testing.T) {
	channel := &recordingChannel{}
	stub := &welcomeStubLLM{text: "来得正好，我们在吵火锅蘸料"}
	runtime := newLLMWelcomeRuntime(BotConfig{
		WelcomeEnabled: true,
		WelcomeMode:    WelcomeModeLLM,
		SystemPrompt:   "一只高冷的猫娘",
	}, channel, stub)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "g-welcome", UserID: "u-old", SenderName: "老群友", MessageID: "m1", RawMessage: "火锅蘸料必须是麻酱", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "火锅蘸料必须是麻酱"}}}, Time: time.Now().Unix()})
	runtime.rememberWelcomeText("", "上一句欢迎")

	event := welcomeNoticeEvent("u1")
	event.SenderName = "小新"
	event.GroupName = "吃货群"
	if err := runtime.handleNotice(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "来得正好，我们在吵火锅蘸料" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	dump := welcomeRequestDump(stub.lastRequest())
	for _, want := range []string{"一只高冷的猫娘", "火锅蘸料必须是麻酱", "【入群通知】小新（", "群「吃货群」", "上一句欢迎"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("welcome request missing %q:\n%s", want, dump)
		}
	}
	if got := runtime.recentWelcomeTexts(""); len(got) != 2 || got[1] != "来得正好，我们在吵火锅蘸料" {
		t.Fatalf("recent welcomes = %#v", got)
	}
}

// 同一条入群通知到两次只欢迎一次，不会先发一句固定文本再发一句问候。
func TestWelcomeDuplicateNoticeWelcomesOnce(t *testing.T) {
	channel := &recordingChannel{}
	stub := &welcomeStubLLM{text: "欢迎欢迎"}
	runtime := newLLMWelcomeRuntime(BotConfig{
		WelcomeEnabled:            true,
		WelcomeMode:               WelcomeModeLLM,
		WelcomeMessage:            "固定欢迎 {user_id}",
		WelcomeLLMCooldownSeconds: 300,
	}, channel, stub)

	for i := 0; i < 2; i++ {
		if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
			t.Fatal(err)
		}
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "欢迎欢迎" {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

// 没配模板池、欢迎词还是内置默认时，LLM 不可用就不发，不回落成那句公告腔。
func TestWelcomeLLMModeSkipsBuiltinFallback(t *testing.T) {
	for _, message := range append([]string{""}, builtinWelcomeMessages...) {
		channel := &recordingChannel{}
		stub := &welcomeStubLLM{err: errors.New("upstream down")}
		runtime := newLLMWelcomeRuntime(BotConfig{
			WelcomeEnabled:            true,
			WelcomeMode:               WelcomeModeLLM,
			WelcomeMessage:            message,
			WelcomeLLMCooldownSeconds: 300,
		}, channel, stub)
		if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
			t.Fatal(err)
		}
		if len(channel.sent) != 0 {
			t.Fatalf("message %q: sent = %#v", message, channel.sent)
		}
	}
}

// 发出去的欢迎按群消息记进历史，群上下文里看得到。
func TestWelcomeRecordedAsGroupMessage(t *testing.T) {
	channel := &recordingChannel{}
	runtime := newWelcomeRuntime(BotConfig{
		WelcomeEnabled: true,
		WelcomeMode:    WelcomeModeFixed,
		WelcomeMessage: "固定欢迎",
	}, channel, nil)
	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
		t.Fatal(err)
	}
	history := runtime.contextHistory(MessageEvent{Kind: EventKindGroup, GroupID: "g-welcome"})
	for _, item := range history {
		if item.Outbound && strings.Contains(item.RawMessage, "固定欢迎") {
			if item.Kind != EventKindGroup || item.GroupID != "g-welcome" {
				t.Fatalf("welcome recorded as %#v", item)
			}
			return
		}
	}
	t.Fatalf("welcome missing from group history: %#v", history)
}

// 模型用 [diana-msg] 分了条就按条发，只有第一条 @ 新人；要求一条发完时合成一条。
// 两种情况下排版标记都不会原样发出去。
func TestWelcomeLLMModeFollowsReplyLayout(t *testing.T) {
	cases := []struct {
		output string
		want   []string
	}{
		{output: "来啦" + notificationSplitMarker + "先坐会儿", want: []string{"来啦", "先坐会儿"}},
		{output: replySingleMarker + "来啦" + notificationSplitMarker + "先坐会儿", want: []string{"来啦\n先坐会儿"}},
	}
	for _, tc := range cases {
		channel := &recordingChannel{}
		runtime := newLLMWelcomeRuntime(BotConfig{
			WelcomeEnabled:      true,
			WelcomeMode:         WelcomeModeLLM,
			SendChunkIntervalMS: 1,
		}, channel, &welcomeStubLLM{text: tc.output})
		if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u1")); err != nil {
			t.Fatal(err)
		}
		if len(channel.sent) != len(tc.want) {
			t.Fatalf("%q: sent = %#v", tc.output, channel.sent)
		}
		for index, msg := range channel.sent {
			if msg.Text != tc.want[index] {
				t.Fatalf("%q: chunk %d = %q", tc.output, index, msg.Text)
			}
			if wantMention := index == 0; (msg.MentionUserID == "u1") != wantMention {
				t.Fatalf("%q: chunk %d mention = %q", tc.output, index, msg.MentionUserID)
			}
		}
	}
}
