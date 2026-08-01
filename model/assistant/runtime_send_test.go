package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedChannel struct {
	mu        sync.Mutex
	sendErrs  []error
	sent      []OutgoingMessage
	apiCalls  []string
	apiParams []map[string]any
	apiErr    error
}

// Connect 封装当前模块的 Connect 逻辑。
func (*scriptedChannel) Connect(context.Context, EventHandler) error { return nil }

// Send 按脚本返回错误并记录成功发送的消息。
func (c *scriptedChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sendErrs) > 0 {
		err := c.sendErrs[0]
		c.sendErrs = c.sendErrs[1:]
		if err != nil {
			return err
		}
	}
	c.sent = append(c.sent, msg)
	return nil
}

// CallAPI 记录 action 调用。
func (c *scriptedChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiCalls = append(c.apiCalls, action)
	c.apiParams = append(c.apiParams, params)
	return nil, c.apiErr
}

// Status 返回空状态。
func (*scriptedChannel) Status() ChannelStatus { return ChannelStatus{} }

// Close 关闭通道。
func (*scriptedChannel) Close() error { return nil }

func withFastSendTiming(t *testing.T) {
	t.Helper()
	oldBackoff, oldInterval := sendRetryBackoff, sendChunkInterval
	sendRetryBackoff, sendChunkInterval = time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		sendRetryBackoff, sendChunkInterval = oldBackoff, oldInterval
	})
}

// TestSendRetriesTransientFailure 验证对应功能场景。
func TestSendRetriesTransientFailure(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{sendErrs: []error{errors.New("ws not connected"), errors.New("ws not connected")}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	err := runtime.send(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, "你好")
	if err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "你好" {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

// TestSendGivesUpAfterRetries 验证对应功能场景。
func TestSendGivesUpAfterRetries(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{sendErrs: []error{errors.New("boom"), errors.New("boom"), errors.New("boom")}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	err := runtime.send(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, "你好")
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("err = %v", err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("sent = %#v", channel.sent)
	}
}

// TestSendOnlyFirstChunkCarriesReplyAndAt 验证对应功能场景。
func TestSendOnlyFirstChunkCarriesReplyAndAt(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{DirectReplyChunkSize: 4, ForwardReplyThreshold: 1000, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1"}

	if err := runtime.send(context.Background(), event, "一二三四五六七"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 2 {
		t.Fatalf("sent %d messages: %#v", len(channel.sent), channel.sent)
	}
	first, second := channel.sent[0], channel.sent[1]
	if first.ReplyMessageID != "m1" || first.MentionUserID != "10001" {
		t.Fatalf("first chunk missing reply/at: %#v", first)
	}
	if second.ReplyMessageID != "" || second.MentionUserID != "" {
		t.Fatalf("second chunk should not @ again: %#v", second)
	}
}

// TestSendLongReplyUsesForward 验证对应功能场景。
func TestSendLongReplyUsesForward(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{BotQQ: "999", DirectReplyChunkSize: 10, ForwardReplyThreshold: 12}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}

	reply := strings.Repeat("长回复内容", 5)
	if err := runtime.send(context.Background(), event, reply); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.apiCalls) != 1 || channel.apiCalls[0] != "send_group_forward_msg" {
		t.Fatalf("apiCalls = %#v", channel.apiCalls)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("should not fall back to direct send: %#v", channel.sent)
	}
	nodes, ok := channel.apiParams[0]["messages"].([]map[string]any)
	if !ok || len(nodes) == 0 {
		t.Fatalf("forward nodes = %#v", channel.apiParams[0])
	}
}

// TestSendForwardFallsBackToChunks 验证对应功能场景。
func TestSendForwardFallsBackToChunks(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{apiErr: errors.New("forward unsupported")}
	runtime := NewRuntime(BotConfig{BotQQ: "999", DirectReplyChunkSize: 10, ForwardReplyThreshold: 12, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	reply := strings.Repeat("长回复内容", 5)
	if err := runtime.send(context.Background(), event, reply); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.apiCalls) != 1 || channel.apiCalls[0] != "send_private_forward_msg" {
		t.Fatalf("apiCalls = %#v", channel.apiCalls)
	}
	if len(channel.sent) == 0 {
		t.Fatal("expected fallback direct sends")
	}
}

// TestRememberReplyEntersAssistantContext 验证对应功能场景。
func TestRememberReplyEntersAssistantContext(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "m1"}

	runtime.remember(event)
	runtime.rememberReply(event, "我刚才说过的话")

	history := runtime.contextHistory(event)
	if len(history) != 2 {
		t.Fatalf("history len = %d", len(history))
	}
	if history[0].botReply != "" || history[1].botReply != "我刚才说过的话" {
		t.Fatalf("history = %#v", history)
	}
}

// TestSystemPromptTogglesDisableInjections 验证对应功能场景。
func TestSystemPromptTogglesDisableInjections(t *testing.T) {
	off := false
	runtime := NewRuntime(BotConfig{
		SystemPrompt:               "自定义人设",
		PromptInjectTime:           &off,
		PromptInjectPlaintextRules: &off,
		PromptInjectGroupSender:    &off,
		PromptChineseSlangHint:     &off,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", SenderName: "小明"}

	prompt := runtime.systemPrompt(event, nil)
	if !strings.HasPrefix(prompt, "自定义人设") {
		t.Fatalf("prompt = %q", prompt)
	}
	for _, banned := range []string{"当前时间", "Markdown", "小明", "谐音梗"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("prompt should omit %q:\n%s", banned, prompt)
		}
	}
}

type stubGroupConfigStore struct {
	configs map[string]GroupConfig
}

// ConfigForGroup 返回预置的群配置。
func (s *stubGroupConfigStore) ConfigForGroup(groupID string) (GroupConfig, bool) {
	cfg, ok := s.configs[groupID]
	return cfg, ok
}

// TestGroupSystemPromptOverridesGlobal 验证对应功能场景。
func TestGroupSystemPromptOverridesGlobal(t *testing.T) {
	runtime := NewRuntime(BotConfig{SystemPrompt: "全局人设"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"777": {GroupID: "777", SystemPrompt: "本群限定的傲娇人设"},
	}})

	groupPrompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "777", UserID: "1"}, nil)
	if !strings.HasPrefix(groupPrompt, "本群限定的傲娇人设") {
		t.Fatalf("group prompt = %q", groupPrompt)
	}
	otherPrompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "888", UserID: "1"}, nil)
	if !strings.HasPrefix(otherPrompt, "全局人设") {
		t.Fatalf("other group prompt = %q", otherPrompt)
	}
}

// TestSendRespectsReferenceAndMentionToggles 验证对应功能场景。
func TestSendRespectsReferenceAndMentionToggles(t *testing.T) {
	withFastSendTiming(t)
	off := false
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{
		ReplyReferenceEnabled: &off,
		MentionUserEnabled:    &off,
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1"}

	if err := runtime.send(context.Background(), event, "你好"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if channel.sent[0].ReplyMessageID != "" || channel.sent[0].MentionUserID != "" {
		t.Fatalf("toggles ignored: %#v", channel.sent[0])
	}
}

// TestSystemPromptInjectsRulesTimeAndSender 验证对应功能场景。
func TestSystemPromptInjectsRulesTimeAndSender(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", SenderName: "小明"}

	prompt := runtime.systemPrompt(event, nil)
	for _, want := range []string{
		"不渲染 Markdown",
		"当前时间：" + time.Now().Format("2006-01-02"),
		"「小明」",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSystemPromptUsesCustomTemplates(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		SystemPrompt:              "自定义人设",
		PromptChineseSlangText:    "自定义中文语境",
		PromptPlaintextRulesText:  "自定义输出规则",
		PromptTimeTemplate:        "时间={datetime}，星期={weekday}",
		PromptGroupSenderTemplate: "当前发言者={sender}",
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	prompt := runtime.systemPrompt(MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "456",
		SenderName: "小明",
	}, nil)

	for _, want := range []string{"自定义人设", "自定义中文语境", "自定义输出规则", "时间=", "星期=", "当前发言者=小明"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "{datetime}") || strings.Contains(prompt, "{weekday}") || strings.Contains(prompt, "{sender}") {
		t.Fatalf("prompt contains unresolved known placeholders:\n%s", prompt)
	}
}

func TestCleanInputUsesCustomFallbackPrompts(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		PromptImageOnlyText: "自定义图片请求",
		PromptWakeOnlyText:  "自定义唤醒回应",
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	imageEvent := MessageEvent{
		Kind:     EventKindPrivate,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/image.png"}}},
	}
	if got := runtime.cleanInput(imageEvent, ""); got != "自定义图片请求" {
		t.Fatalf("image fallback = %q", got)
	}
	if got := runtime.cleanInput(MessageEvent{Kind: EventKindPrivate}, ""); got != "自定义唤醒回应" {
		t.Fatalf("wake fallback = %q", got)
	}
}
