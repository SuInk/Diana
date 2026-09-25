// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// welcomeStubLLM 记录调用次数并返回固定结果，用于验证冷却与回落行为。
type welcomeStubLLM struct {
	mu    sync.Mutex
	calls int
	text  string
	err   error
}

func (s *welcomeStubLLM) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &llm.GenerateResponse{Text: s.text}, nil
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
