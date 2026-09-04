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
)

type scriptedChannel struct {
	mu        sync.Mutex
	sendErrs  []error
	sent      []OutgoingMessage
	apiCalls  []string
	apiParams []map[string]any
	apiErr    error
}

type replyBatchChannel struct {
	mu        sync.Mutex
	sent      []string
	firstSent chan struct{}
	firstOnce sync.Once
}

func (*replyBatchChannel) Connect(context.Context, EventHandler) error { return nil }
func (*replyBatchChannel) Close() error                                { return nil }
func (*replyBatchChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (*replyBatchChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

func (c *replyBatchChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	c.sent = append(c.sent, msg.Text)
	c.mu.Unlock()
	if msg.Text == "A1" {
		c.firstOnce.Do(func() { close(c.firstSent) })
	}
	return nil
}

func (c *replyBatchChannel) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sent...)
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
	runtime := NewRuntime(BotConfig{DirectReplyChunkSize: 4, ForwardReplyThreshold: 1000, SendChunkIntervalMS: 1,
		ReplyReferenceMode: ReplyDecorationOn, MentionUserMode: ReplyDecorationOn}, channel, NewPluginManager(), nil, nil, nil, nil)
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

func TestReplyBatchesDoNotInterleaveWithinSession(t *testing.T) {
	channel := &replyBatchChannel{firstSent: make(chan struct{})}
	runtime := NewRuntime(BotConfig{SendChunkIntervalMS: 50}, channel, NewPluginManager(), nil, nil, nil, nil)
	eventA := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "a"}
	eventB := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10002", MessageID: "b"}

	errCh := make(chan error, 2)
	go func() {
		errCh <- runtime.send(context.Background(), eventA, "A1"+notificationSplitMarker+"A2")
	}()
	<-channel.firstSent
	go func() {
		errCh <- runtime.send(context.Background(), eventB, "B1"+notificationSplitMarker+"B2")
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("send() error = %v", err)
		}
	}

	if got, want := strings.Join(channel.snapshot(), ","), "A1,A2,B1,B2"; got != want {
		t.Fatalf("reply batches interleaved: got %s, want %s", got, want)
	}
	runtime.replyBatchMu.Lock()
	defer runtime.replyBatchMu.Unlock()
	if len(runtime.replyBatches) != 0 {
		t.Fatalf("completed reply batch gates were not cleaned up: %#v", runtime.replyBatches)
	}
}

func TestReplyBatchesRemainConcurrentAcrossSessions(t *testing.T) {
	channel := &replyBatchChannel{firstSent: make(chan struct{})}
	runtime := NewRuntime(BotConfig{SendChunkIntervalMS: 200}, channel, NewPluginManager(), nil, nil, nil, nil)
	eventA := MessageEvent{Kind: EventKindGroup, GroupID: "group-a", UserID: "10001", MessageID: "a"}
	eventB := MessageEvent{Kind: EventKindGroup, GroupID: "group-b", UserID: "10002", MessageID: "b"}

	aDone := make(chan error, 1)
	go func() {
		aDone <- runtime.send(context.Background(), eventA, "A1"+notificationSplitMarker+"A2")
	}()
	<-channel.firstSent
	bDone := make(chan error, 1)
	go func() {
		bDone <- runtime.send(context.Background(), eventB, "B1")
	}()

	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("second session delivery failed: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("unrelated sessions were serialized behind one global reply batch")
	}
	if err := <-aDone; err != nil {
		t.Fatalf("first session delivery failed: %v", err)
	}
}

// TestSendLongReplyUsesForward 验证对应功能场景。
func TestSendLongReplyUsesForward(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "999", DirectReplyChunkSize: 10, ForwardReplyThreshold: 12}, channel, NewPluginManager(), nil, nil, nil, nil)
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
	runtime := NewRuntime(BotConfig{BotAccount: "999", DirectReplyChunkSize: 10, ForwardReplyThreshold: 12, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, nil)
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

func TestPrivateOutgoingHistoryKeepsPeerSessionAndAssistantRole(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	source := MessageEvent{Kind: EventKindPrivate, SelfID: "42", UserID: "10001", MessageID: "incoming-1"}

	runtime.remember(source)
	runtime.rememberOutgoingWithMessageID(context.Background(), source, OutgoingMessage{Text: "我刚才说过的话"}, "outgoing-1")

	history := runtime.contextHistory(source)
	if len(history) != 2 {
		t.Fatalf("history len = %d, history = %#v", len(history), history)
	}
	outgoing := history[1]
	if outgoing.UserID != "10001" || outgoing.SelfID != "42" || !outgoing.Outbound {
		t.Fatalf("private outgoing history = %#v", outgoing)
	}
	if !assistantHistoryEvent(outgoing, "42") {
		t.Fatalf("private outgoing history was not recognized as assistant: %#v", outgoing)
	}
	if sessionKey(outgoing) != sessionKey(source) {
		t.Fatalf("private outgoing session = %q, want %q", sessionKey(outgoing), sessionKey(source))
	}
}

func TestErrorWrapperDoesNotEnterModelHistory(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "source"}
	runtime.rememberReply(event, "出错了：图片读取失败，请重新发送图片后再试。")
	if got := historyPromptText(runtime.contextHistory(event)[0]); got != "" {
		t.Fatalf("error wrapper leaked into history prompt: %q", got)
	}
	if got := compactContextEvent(runtime.contextHistory(event)[0]); got != "" {
		t.Fatalf("error wrapper leaked into context summary: %q", got)
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
	for _, banned := range []string{"当前时间", "Markdown", "小明", "谐音梗", "有画面感"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("prompt should omit %q:\n%s", banned, prompt)
		}
	}
}

type stubGroupConfigStore struct {
	configs map[string]GroupConfig
}

// ConfigForGroup 返回预置的群配置。
func (s *stubGroupConfigStore) ConfigForGroup(_, groupID string) (GroupConfig, bool) {
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
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{
		ReplyReferenceMode: ReplyDecorationOff,
		MentionUserMode:    ReplyDecorationOff,
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
		"「小明」",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// 时间已移出人设提示词，改由尾部独立 system 消息承载。
	if clock := runtime.runtimeClockPrompt(event); !strings.Contains(clock, "当前时间："+time.Now().Format("2006-01-02")) {
		t.Fatalf("clock prompt missing rendered time: %q", clock)
	}
}

func TestSystemPromptInjectsOnlyCurrentPlatformOutputRules(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		want     string
		unwanted string
	}{
		{name: "onebot", platform: PlatformOneBotV11, want: "OneBot v11 消息不渲染 Markdown", unwanted: "不要声称当前窗口不支持 Markdown"},
		{name: "telegram", platform: PlatformTelegram, want: "当前聊天平台是 Telegram", unwanted: "OneBot v11 消息不渲染 Markdown"},
		{name: "dingtalk", platform: PlatformDingTalk, want: "当前聊天平台是 钉钉", unwanted: "OneBot v11 消息不渲染 Markdown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{Platform: test.platform}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, nil)
			if !strings.Contains(prompt, test.want) || strings.Contains(prompt, test.unwanted) {
				t.Fatalf("platform prompt mismatch:\n%s", prompt)
			}
		})
	}
}

func TestSystemPromptUsesPlaintextOverrideOnlyWhenEnabled(t *testing.T) {
	on, off := true, false
	for _, test := range []struct {
		name string
		flag *bool
		want bool
	}{
		{name: "enabled", flag: &on, want: true},
		{name: "disabled", flag: &off, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{Platform: PlatformTelegram, MarkdownToPlain: test.flag, PromptPlaintextRulesText: "只发自定义纯文本"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, nil)
			if got := strings.Contains(prompt, "只发自定义纯文本"); got != test.want {
				t.Fatalf("custom plaintext present=%t, want %t:\n%s", got, test.want, prompt)
			}
		})
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

	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "456",
		SenderName: "小明",
	}
	prompt := runtime.systemPrompt(event, nil)

	for _, want := range []string{"自定义人设", "自定义中文语境", "自定义输出规则", "当前发言者=小明"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// PromptTimeTemplate 仍然生效，只是渲染到尾部时钟消息而不是人设提示词。
	clock := runtime.runtimeClockPrompt(event)
	for _, want := range []string{"时间=", "星期="} {
		if !strings.Contains(clock, want) {
			t.Fatalf("clock prompt missing %q:\n%s", want, clock)
		}
	}
	for _, text := range []string{prompt, clock} {
		if strings.Contains(text, "{datetime}") || strings.Contains(text, "{weekday}") || strings.Contains(text, "{sender}") {
			t.Fatalf("prompt contains unresolved known placeholders:\n%s", text)
		}
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

// 错误提示不能被人格预设的短句切分拦腰截断。群友风格把每条聊天压到 160 字，
// 上游返回的报错往往比这长，切开之后报错和后面那个说明链接会落进两条消息里，
// 看起来像机器人自己断句断错了。
func TestErrorNoticeIsNotChunkedByPersonaStyle(t *testing.T) {
	withFastSendTiming(t)
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		ResponseMode: ResponseModeStandard,
		ReplyStyle:   ReplyStyleGroupmate,
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	if size := runtime.Config().DirectReplyChunkSize; size != chatReplyChunkSize {
		t.Fatalf("fixture needs the groupmate chunk size, got %d", size)
	}

	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1"}
	notice := "出错了：llm: provider request failed: llm: openai-compatible request failed: 403 Forbidden: " +
		"type=error; message=The latest version of this model is only available hosted in China and " +
		"requires explicit opt in: https://example.test/docs/opt-in. " +
		"Retrying will keep failing until the account owner accepts the regional terms for this model, " +
		"switches the profile to a model that is available in the current region, or routes the request " +
		"through a provider endpoint that already carries the required opt-in flag."
	if len([]rune(notice)) <= runtime.Config().DirectReplyChunkSize {
		t.Fatalf("fixture notice must be longer than the chat chunk size: %d runes", len([]rune(notice)))
	}

	if _, _, err := runtime.sendErrorNoticeWithEvidence(context.Background(), event, notice); err != nil {
		t.Fatalf("send error notice: %v", err)
	}
	channel.mu.Lock()
	sent := append([]OutgoingMessage(nil), channel.sent...)
	channel.mu.Unlock()
	if len(sent) != 1 {
		texts := make([]string, 0, len(sent))
		for _, message := range sent {
			texts = append(texts, message.Text)
		}
		t.Fatalf("error notice was split into %d messages: %#v", len(sent), texts)
	}
	if sent[0].Text != notice {
		t.Fatalf("error notice was rewritten: %q", sent[0].Text)
	}
}
