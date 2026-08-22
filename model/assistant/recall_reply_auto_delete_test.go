// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecallReplyAutoDeleteConfigDefaultsDisabledAndCanBeEnabled(t *testing.T) {
	defaults := (BotConfig{}).WithDefaults()
	if defaults.RecallReplyMode != RecallReplyModeLLMSummary {
		t.Fatalf("default recall reply mode = %q", defaults.RecallReplyMode)
	}
	if defaults.RecallReplyAutoDeleteEnabled == nil || *defaults.RecallReplyAutoDeleteEnabled {
		t.Fatalf("default config = %#v", defaults.RecallReplyAutoDeleteEnabled)
	}
	if defaults.RecallReplyTTLSeconds != defaultRecallReplyTTLSeconds {
		t.Fatalf("default delay = %d", defaults.RecallReplyTTLSeconds)
	}

	enabled := true
	payload := PayloadFromConfig(BotConfig{
		RecallReplyMode:              RecallReplyModeOriginalForward,
		RecallReplyAutoDeleteEnabled: &enabled,
		RecallReplyTTLSeconds:        75,
	})
	got := ConfigFromPayload(payload, BotConfig{})
	if got.RecallReplyMode != RecallReplyModeOriginalForward {
		t.Fatalf("recall reply mode did not round-trip: %q", got.RecallReplyMode)
	}
	if got.RecallReplyAutoDeleteEnabled == nil || !*got.RecallReplyAutoDeleteEnabled {
		t.Fatalf("enabled config did not round-trip: %#v", got.RecallReplyAutoDeleteEnabled)
	}
	if got.RecallReplyTTLSeconds != 75 {
		t.Fatalf("delay did not round-trip: %d", got.RecallReplyTTLSeconds)
	}
	if clamped := (BotConfig{RecallReplyTTLSeconds: maximumRecallReplyTTLSeconds + 1}).WithDefaults(); clamped.RecallReplyTTLSeconds != maximumRecallReplyTTLSeconds {
		t.Fatalf("clamped delay = %d", clamped.RecallReplyTTLSeconds)
	}
}

func TestGroupConfigOverridesRecallReplyAutoDeletePolicy(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name         string
		baseEnabled  *bool
		baseDelay    int
		groupEnabled *bool
		groupDelay   int
		wantEnabled  bool
		wantDelay    int
	}{
		{name: "inherits disabled global defaults", baseEnabled: &disabled, baseDelay: 60, wantEnabled: false, wantDelay: 60},
		{name: "group disables cleanup", baseEnabled: &enabled, baseDelay: 60, groupEnabled: &disabled, groupDelay: 90, wantEnabled: false, wantDelay: 90},
		{name: "group enables custom cleanup", baseEnabled: &disabled, baseDelay: 60, groupEnabled: &enabled, groupDelay: 120, wantEnabled: true, wantDelay: 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := BotConfig{
				RecallReplyAutoDeleteEnabled: tt.baseEnabled,
				RecallReplyTTLSeconds:        tt.baseDelay,
			}
			store := &testWritableGroupConfigStore{}
			_, err := store.SaveGroupConfig(GroupConfig{
				GroupID:                      "123",
				Enabled:                      true,
				EnabledSet:                   true,
				RecallReplyAutoDeleteEnabled: tt.groupEnabled,
				RecallReplyTTLSeconds:        tt.groupDelay,
			}, base)
			if err != nil {
				t.Fatal(err)
			}
			runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetGroupConfigStore(store)
			effective := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "123"})
			if effective.RecallReplyAutoDeleteEnabled == nil || *effective.RecallReplyAutoDeleteEnabled != tt.wantEnabled {
				t.Fatalf("effective enabled = %#v", effective.RecallReplyAutoDeleteEnabled)
			}
			if effective.RecallReplyTTLSeconds != tt.wantDelay {
				t.Fatalf("effective delay = %d", effective.RecallReplyTTLSeconds)
			}
			responses := []PluginResponse{{RecallDisclosure: true}}
			if recallReplyShouldAutoDelete(effective, responses) != tt.wantEnabled {
				t.Fatal("auto-delete decision did not match effective config")
			}
			if got := recallReplyAutoDeleteDelay(effective); got != time.Duration(tt.wantDelay)*time.Second {
				t.Fatalf("auto-delete delay = %s", got)
			}
		})
	}
}

func TestRecallReplyAutoDeleteHonorsGroupPolicy(t *testing.T) {
	// 这个用例以前是端到端的：靠词表让插件劫持回复，再断言撤回上下文进了 LLM 请求。
	// 触发权交给模型之后，撤回记录由 diana.chat_history 的 recalls 操作读回，响应经
	// recallDisclosureSink 合并进本轮 pluginResponses——自动撤回策略读的仍是同一处，
	// 所以这里直接验证那个接缝，不再依赖一次伪造的 LLM 往返。
	enabled := true
	disabled := false
	tests := []struct {
		name        string
		groupPolicy *bool
		wantDelete  bool
		wantDelay   time.Duration
	}{
		{name: "enabled", groupPolicy: &enabled, wantDelete: true, wantDelay: time.Second},
		{name: "disabled", groupPolicy: &disabled, wantDelete: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{
				RecallReplyMode:              RecallReplyModeOriginalForward,
				RecallReplyAutoDeleteEnabled: &disabled,
				RecallReplyTTLSeconds:        60,
			}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
			store := &testWritableGroupConfigStore{}
			if _, err := store.SaveGroupConfig(GroupConfig{
				GroupID:                      "123",
				Enabled:                      true,
				EnabledSet:                   true,
				RecallReplyAutoDeleteEnabled: tt.groupPolicy,
				RecallReplyTTLSeconds:        1,
			}, runtime.Config()); err != nil {
				t.Fatal(err)
			}
			runtime.SetGroupConfigStore(store)

			event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "456", MessageID: "source-1"}
			cfg := runtime.effectiveConfigForEvent(event)

			sink := &recallDisclosureSink{}
			sink.add(PluginResponse{Handled: true, RecallDisclosure: true})
			disclosures := applyRecallReplyMode(sink.drain(), cfg.RecallReplyMode)

			if got := recallReplyShouldAutoDelete(cfg, disclosures); got != tt.wantDelete {
				t.Fatalf("auto delete = %v, want %v", got, tt.wantDelete)
			}
			if tt.wantDelete {
				if got := recallReplyAutoDeleteDelay(cfg); got != tt.wantDelay {
					t.Fatalf("auto delete delay = %v, want %v", got, tt.wantDelay)
				}
			}
		})
	}
}

func TestRecallDisclosureSinkKeepsOneResponsePerTurn(t *testing.T) {
	// 模型可能在一轮里多次调用 recalls；转发卡片只能发一次，否则同一批撤回记录会
	// 被重复投递。
	sink := &recallDisclosureSink{}
	sink.add(PluginResponse{Handled: true, RecallDisclosure: true, Reply: "first"})
	sink.add(PluginResponse{Handled: true, RecallDisclosure: true, Reply: "second"})

	drained := sink.drain()
	if len(drained) != 1 || drained[0].Reply != "first" {
		t.Fatalf("drained = %#v", drained)
	}
	if again := sink.drain(); len(again) != 0 {
		t.Fatalf("sink was not emptied: %#v", again)
	}
}

func TestMessageHistoryPluginMarksOnlyRecallQueriesForAutoDelete(t *testing.T) {
	plugin := NewMessageHistoryPlugin()
	plugin.Observe(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "20002",
		MessageID:  "old-1",
		RawMessage: "撤回前的内容",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "撤回前的内容"}}},
	})
	plugin.Observe(context.Background(), MessageEvent{
		Kind:      EventKindNotice,
		SubType:   "group_recall",
		GroupID:   "123",
		UserID:    "20002",
		MessageID: "old-1",
	})

	// 「这条消息算不算在问撤回」现在由模型判断：Handle 不再靠词表劫持回复，普通
	// 消息自然拿不到 disclosure，因为模型压根不会去调 recalls 操作。
	if normal, err := plugin.Handle(context.Background(), PluginRequest{
		Event: MessageEvent{Kind: EventKindGroup, GroupID: "123"}, Text: "查看刚才撤回的消息",
	}); err != nil || normal != nil {
		t.Fatalf("plugin still hijacks replies: resp=%#v err=%v", normal, err)
	}
	query := recallDisclosureForTest(t, plugin, PluginRequest{Event: MessageEvent{Kind: EventKindGroup, GroupID: "123"}})
	if query == nil || !query.RecallDisclosure {
		t.Fatalf("recall query response = %#v", query)
	}
	if recallReplyShouldAutoDelete(BotConfig{}, []PluginResponse{*query}) {
		t.Fatal("default recall disclosure should not auto-delete")
	}
	enabled := true
	if !recallReplyShouldAutoDelete(BotConfig{RecallReplyAutoDeleteEnabled: &enabled}, []PluginResponse{*query}) {
		t.Fatal("explicitly enabled recall disclosure should auto-delete")
	}
}

func TestRuntimeCollectsSentMessageIDAndDeletesItAfterDelay(t *testing.T) {
	channel := newRecallDeleteChannel()
	runtime := NewRuntime(BotConfig{DirectReplyChunkSize: 900, ForwardReplyThreshold: 900}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "456", MessageID: "source-1"}

	messageIDs, err := runtime.sendWithMessageIDs(context.Background(), event, "撤回记录内容")
	if err != nil {
		t.Fatal(err)
	}
	if len(messageIDs) != 1 || messageIDs[0] != "101" {
		t.Fatalf("message ids = %#v", messageIDs)
	}
	history := runtime.contextHistory(event)
	if len(history) != 1 || history[0].MessageID != "101" || history[0].RawMessage != "撤回记录内容" {
		t.Fatalf("outgoing history = %#v", history)
	}
	runtime.scheduleMessageDeletes(event, messageIDs, 5*time.Millisecond)

	select {
	case deleted := <-channel.deleted:
		if deleted != int64(101) {
			t.Fatalf("deleted message id = %#v", deleted)
		}
	case <-time.After(time.Second):
		t.Fatal("delete_msg was not called")
	}
}

func TestRuntimeDeletesRecallReplyThroughSourceProfile(t *testing.T) {
	first := newRecallDeleteChannel()
	second := newRecallDeleteChannel()
	runtime := NewRuntime(BotConfig{ID: "onebot-second", Platform: PlatformOneBotV11}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "onebot-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "onebot-second", Platform: PlatformOneBotV11, Channel: second},
	}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "onebot-first", Platform: PlatformOneBotV11, GroupID: "123"}

	runtime.scheduleMessageDeletes(event, []string{"101"}, 0)
	select {
	case deleted := <-first.deleted:
		if deleted != int64(101) {
			t.Fatalf("deleted message id = %#v", deleted)
		}
	case <-time.After(time.Second):
		t.Fatal("source profile did not receive delete_msg")
	}
	select {
	case deleted := <-second.deleted:
		t.Fatalf("other profile received delete_msg: %#v", deleted)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRuntimeCollectsForwardMessageID(t *testing.T) {
	channel := newRecallDeleteChannel()
	runtime := NewRuntime(BotConfig{BotAccount: "42", DirectReplyChunkSize: 10, ForwardReplyThreshold: 5}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "456", MessageID: "source-1", SelfID: "42"}

	messageIDs, err := runtime.sendWithMessageIDs(context.Background(), event, "这是一条超过合并转发阈值的撤回记录")
	if err != nil {
		t.Fatal(err)
	}
	if len(messageIDs) != 1 || messageIDs[0] != "901" {
		t.Fatalf("forward message ids = %#v", messageIDs)
	}
	history := runtime.contextHistory(event)
	if len(history) != 1 || history[0].MessageID != "901" || !strings.Contains(strings.ReplaceAll(history[0].RawMessage, "\n", ""), "超过合并转发阈值") {
		t.Fatalf("forward outgoing history = %#v", history)
	}
}

type recallDeleteChannel struct {
	mu      sync.Mutex
	nextID  int64
	deleted chan any
}

func newRecallDeleteChannel() *recallDeleteChannel {
	return &recallDeleteChannel{nextID: 100, deleted: make(chan any, 4)}
}

func (c *recallDeleteChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *recallDeleteChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}
func (c *recallDeleteChannel) SendWithResult(context.Context, OutgoingMessage) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return map[string]any{"message_id": c.nextID}, nil
}
func (c *recallDeleteChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "delete_msg" {
		c.deleted <- params["message_id"]
		return map[string]any{}, nil
	}
	if action == "send_group_forward_msg" || action == "send_private_forward_msg" {
		return map[string]any{"message_id": int64(901)}, nil
	}
	return map[string]any{}, nil
}
func (c *recallDeleteChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, SelfID: "42"}
}
func (c *recallDeleteChannel) Close() error { return nil }

func TestChatHistoryRecallsOperationFeedsForwardCardBackIntoTheTurn(t *testing.T) {
	history := NewMessageHistoryPlugin()
	history.Observe(context.Background(), MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "20002", MessageID: "old-1",
		RawMessage: "撤回前的完整内容", SenderName: "Alice",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "撤回前的完整内容"}}},
	})
	history.Observe(context.Background(), messageEventFromEnvelope(oneBotEnvelope{
		PostType: "notice", NoticeType: "group_recall", GroupID: "123", UserID: "20002", MessageID: "old-1",
	}))
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(history), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "query-1"}

	sink := &recallDisclosureSink{}
	raw, err := newDianaChatHistoryTool(runtime, event).withRecallSink(sink).
		Run(context.Background(), map[string]any{"operation": "recalls"})
	if err != nil {
		t.Fatal(err)
	}
	// 模型拿到的是数据，用来写一句说明。
	if !strings.Contains(raw, "撤回前的完整内容") || !strings.Contains(raw, "recalls") {
		t.Fatalf("tool output = %s", raw)
	}
	// 转发卡片仍由回复阶段沿用既有链路投递，所以响应必须被交回本轮。
	drained := sink.drain()
	if len(drained) != 1 || !drained[0].RecallDisclosure || len(drained[0].ForwardMessages) == 0 {
		t.Fatalf("sink = %#v", drained)
	}
}

func TestChatHistoryRecallsOperationRejectsPrivateChats(t *testing.T) {
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(NewMessageHistoryPlugin()), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "query-1"}

	if _, err := newDianaChatHistoryTool(runtime, event).withRecallSink(&recallDisclosureSink{}).
		Run(context.Background(), map[string]any{"operation": "recalls"}); err == nil {
		t.Fatal("recalls should be rejected outside group chats")
	}
}
