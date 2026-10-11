// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type recallPersistenceStore struct {
	events map[string][]MessageEvent
}

func newRecallPersistenceStore() *recallPersistenceStore {
	return &recallPersistenceStore{events: map[string][]MessageEvent{}}
}

func (s *recallPersistenceStore) AppendMessageEvent(_ context.Context, session string, event MessageEvent) error {
	s.events[session] = append(s.events[session], event)
	return nil
}

func (s *recallPersistenceStore) ListRecentMessageEvents(_ context.Context, session string, limit int) ([]MessageEvent, error) {
	events := append([]MessageEvent(nil), s.events[session]...)
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (s *recallPersistenceStore) FindMessageEvent(_ context.Context, session string, messageID string) (MessageEvent, bool, error) {
	events := s.events[session]
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != EventKindNotice && events[i].MessageID == messageID {
			return events[i], true, nil
		}
	}
	return MessageEvent{}, false, nil
}

func (s *recallPersistenceStore) ListGroupRecallEvents(_ context.Context, groupID string) ([]MessageEvent, error) {
	var out []MessageEvent
	for _, event := range s.events["group:"+groupID] {
		if isRecallNotice(event) && event.GroupID == groupID {
			out = append(out, event)
		}
	}
	return out, nil
}

func TestRecallHistoryKeepsTelegramBotScopesSeparate(t *testing.T) {
	store := newRecallPersistenceStore()
	store.events["group:-100123"] = []MessageEvent{
		{
			Platform: PlatformTelegram, ProfileID: "telegram-a", SelfID: "9000",
			Kind: EventKindNotice, SubType: "group_recall", Time: 101,
			GroupID: "-100123", UserID: "user-a", MessageID: "same-id", RawMessage: "A 看到的撤回",
		},
		{
			Platform: PlatformTelegram, ProfileID: "telegram-b", SelfID: "9000",
			Kind: EventKindNotice, SubType: "group_recall", Time: 102,
			GroupID: "-100123", UserID: "user-b", MessageID: "same-id", RawMessage: "其他 ProfileID 的撤回不能泄漏",
		},
		{
			Platform: PlatformTelegram, ProfileID: "telegram-a", SelfID: "9001",
			Kind: EventKindNotice, SubType: "group_recall", Time: 103,
			GroupID: "-100123", UserID: "user-c", MessageID: "same-id", RawMessage: "其他 SelfID 的撤回不能泄漏",
		},
	}
	runtime := NewRuntime(BotConfig{ID: "telegram-a", Platform: PlatformTelegram, BotAccount: "9000"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	current := MessageEvent{
		Platform: PlatformTelegram, ProfileID: "telegram-a", SelfID: "9000",
		Kind: EventKindGroup, Time: 200, GroupID: "-100123", UserID: "requester",
	}

	recalls := runtime.recallHistory(current)
	if len(recalls) != 1 || recalls[0].RawMessage != "A 看到的撤回" {
		t.Fatalf("scoped recalls = %#v", recalls)
	}
	plugin := NewMessageHistoryPlugin()
	plugin.Observe(context.Background(), store.events["group:-100123"][0])
	plugin.Observe(context.Background(), store.events["group:-100123"][1])
	plugin.Observe(context.Background(), store.events["group:-100123"][2])
	response, disclosed, _ := plugin.RecallDisclosureResponse(
		context.Background(), nil, current, nil, store.events["group:-100123"],
	)
	if response == nil || len(disclosed) != 1 || strings.Contains(response.Context, "不能泄漏") {
		t.Fatalf("disclosure leaked another Telegram bot: response=%#v recalls=%#v", response, disclosed)
	}
}

func TestRecallHistoryKeepsLegacyOneBotRecordsWithoutBotIdentity(t *testing.T) {
	store := newRecallPersistenceStore()
	store.events["group:123"] = []MessageEvent{{
		Kind: EventKindNotice, SubType: "group_recall", Time: 101,
		GroupID: "123", UserID: "20002", MessageID: "legacy-recall", RawMessage: "旧版未写机器人身份的撤回",
	}}
	runtime := NewRuntime(BotConfig{ID: "onebot-a", Platform: PlatformOneBotV11, BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	current := MessageEvent{
		Platform: PlatformOneBotV11, ProfileID: "onebot-a", SelfID: "42",
		Kind: EventKindGroup, Time: 200, GroupID: "123", UserID: "requester",
	}

	recalls := runtime.recallHistory(current)
	if len(recalls) != 1 || recalls[0].MessageID != "legacy-recall" {
		t.Fatalf("legacy OneBot recall was filtered out: %#v", recalls)
	}
	response, disclosed, _ := NewMessageHistoryPlugin().RecallDisclosureResponse(
		context.Background(), nil, current, nil, recalls,
	)
	if response == nil || len(disclosed) != 1 || !strings.Contains(response.Context, "旧版未写机器人身份的撤回") {
		t.Fatalf("legacy OneBot disclosure = %#v recalls=%#v", response, disclosed)
	}
}

func TestTelegramRecallDisclosureRestoresPersistedNoticeItemsAfterRestart(t *testing.T) {
	store := newRecallPersistenceStore()
	store.events["group:-100123"] = []MessageEvent{{
		Platform: PlatformTelegram, ProfileID: "telegram-a", SelfID: "9000",
		Kind: EventKindNotice, SubType: "group_recall", Time: 1_700_000_100,
		OriginalTime: 1_700_000_000, GroupID: "-100123", UserID: "user-a",
		MessageID: "persisted-recall", RawMessage: "重启后仍能交给模型的缓存原文",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "重启后仍能交给模型的缓存原文"}}},
		SenderName: "Alice", OperatorRole: telegramRecallProbeOperatorRole,
	}}
	runtime := NewRuntime(BotConfig{
		ID: "telegram-a", Platform: PlatformTelegram, BotAccount: "9000",
		RecallReplyMode: RecallReplyModeLLMSummary,
	}, nilChannel{}, NewPluginManager(NewMessageHistoryPlugin()), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	current := MessageEvent{
		Platform: PlatformTelegram, ProfileID: "telegram-a", ContextNamespace: "telegram-a", SelfID: "9000",
		Kind: EventKindGroup, Time: 1_700_000_110, GroupID: "-100123", UserID: "requester", MessageID: "99",
	}
	sink := &recallDisclosureSink{}

	result, err := newDianaChatHistoryTool(runtime, current).withRecallSink(sink).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Text != "重启后仍能交给模型的缓存原文" ||
		result.Items[0].Status != "unavailable_at_probe" || result.Items[0].OriginalTime != 1_700_000_000 {
		t.Fatalf("persisted recall items = %#v", result.Items)
	}
	disclosures := applyRecallReplyMode(sink.drain(), RecallReplyModeLLMSummary)
	if len(disclosures) != 1 || len(disclosures[0].ForwardMessages) != 0 || !strings.Contains(disclosures[0].Context, "重启后仍能交给模型的缓存原文") {
		t.Fatalf("summary-only persisted disclosure = %#v", disclosures)
	}
}

func TestRuntimePersistsRecallIndependentlyAndPluginRestoresIt(t *testing.T) {
	store := newRecallPersistenceStore()
	runtime := NewRuntime(BotConfig{DisabledGroups: []string{"123"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	original := MessageEvent{
		Kind:       EventKindGroup,
		Time:       100,
		GroupID:    "123",
		UserID:     "20002",
		MessageID:  "old-1",
		RawMessage: "重启后仍要看到的撤回内容",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "重启后仍要看到的撤回内容"}}},
		SenderName: "Alice",
	}
	if err := runtime.HandleEvent(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if err := runtime.HandleEvent(context.Background(), MessageEvent{
		Kind:      EventKindNotice,
		SubType:   "group_recall",
		Time:      101,
		GroupID:   "123",
		UserID:    "20002",
		MessageID: "old-1",
	}); err != nil {
		t.Fatal(err)
	}

	events := store.events["group:123"]
	if len(events) != 2 {
		t.Fatalf("persisted events = %#v", events)
	}
	recall := events[1]
	if !isRecallNotice(recall) || recall.RawMessage != original.RawMessage || recall.SenderName != "Alice" {
		t.Fatalf("persisted recall = %#v", recall)
	}

	restored, err := store.ListGroupRecallEvents(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	plugin := NewMessageHistoryPlugin()
	resp := recallDisclosureForTest(t, plugin, PluginRequest{
		Event:        MessageEvent{Kind: EventKindGroup, GroupID: "123"},
		RecallEvents: restored,
		Text:         "撤回了什么",
	})
	if resp == nil || !strings.Contains(resp.Context, original.RawMessage) || !strings.Contains(resp.Context, "Alice") {
		t.Fatalf("restored response = %#v", resp)
	}
}

func TestRuntimeRecoversMissingRecallContentFromOneBot(t *testing.T) {
	store := newRecallPersistenceStore()
	channel := &recallGetMsgChannel{}
	runtime := NewRuntime(BotConfig{DisabledGroups: []string{"123"}}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	if err := runtime.HandleEvent(context.Background(), MessageEvent{
		Kind:       EventKindNotice,
		SubType:    "group_recall",
		Time:       200,
		SelfID:     "bot",
		GroupID:    "123",
		UserID:     "20002",
		OperatorID: "admin",
		MessageID:  "remote-1",
	}); err != nil {
		t.Fatal(err)
	}

	events := store.events["group:123"]
	if channel.calls != 1 || len(events) != 2 {
		t.Fatalf("calls=%d events=%#v", channel.calls, events)
	}
	if events[0].Kind != EventKindGroup || events[0].RawMessage != "接入端补回的正文" {
		t.Fatalf("recovered original = %#v", events[0])
	}
	recall := events[1]
	if recall.Kind != EventKindNotice || recall.RawMessage != "接入端补回的正文" || recall.OriginalTime != 150 || recall.Time != 200 || recall.OperatorID != "admin" {
		t.Fatalf("recovered recall = %#v", recall)
	}
}

func TestRuntimeDoesNotPersistBotOwnRecall(t *testing.T) {
	store := newRecallPersistenceStore()
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	if err := runtime.HandleEvent(context.Background(), MessageEvent{
		Kind:       EventKindNotice,
		SubType:    "group_recall",
		Time:       200,
		SelfID:     "bot",
		GroupID:    "123",
		UserID:     "bot",
		OperatorID: "bot",
		MessageID:  "bot-message",
	}); err != nil {
		t.Fatal(err)
	}
	if events := store.events["group:123"]; len(events) != 0 {
		t.Fatalf("bot's own recall should not be recorded: %#v", events)
	}
}

func TestRuntimePersistsBotMessageRecalledByAdministrator(t *testing.T) {
	store := newRecallPersistenceStore()
	store.events["group:123"] = []MessageEvent{{
		Kind:       EventKindGroup,
		Time:       150,
		SelfID:     "bot",
		GroupID:    "123",
		UserID:     "bot",
		MessageID:  "bot-message",
		RawMessage: "机器人原消息",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "机器人原消息"}}},
		SenderName: "Diana",
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	if err := runtime.HandleEvent(context.Background(), MessageEvent{
		Kind:       EventKindNotice,
		SubType:    "group_recall",
		Time:       200,
		SelfID:     "bot",
		GroupID:    "123",
		UserID:     "bot",
		OperatorID: "admin",
		MessageID:  "bot-message",
	}); err != nil {
		t.Fatal(err)
	}
	events := store.events["group:123"]
	if len(events) != 2 || !isRecallNotice(events[1]) || events[1].OperatorID != "admin" {
		t.Fatalf("administrator recall was not recorded: %#v", events)
	}
}

func TestRuntimePersistsSentMessageWithOneBotIDForAdministratorRecall(t *testing.T) {
	store := newRecallPersistenceStore()
	channel := newRecallDeleteChannel()
	runtime := NewRuntime(BotConfig{BotAccount: "42", Name: "Diana"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	source := MessageEvent{
		Kind:      EventKindGroup,
		Time:      100,
		SelfID:    "42",
		GroupID:   "123",
		UserID:    "456",
		MessageID: "source-1",
	}

	messageIDs, err := runtime.sendWithMessageIDs(context.Background(), source, "机器人发出的原消息")
	if err != nil {
		t.Fatal(err)
	}
	if len(messageIDs) != 1 || messageIDs[0] != "101" {
		t.Fatalf("sent message ids = %#v", messageIDs)
	}
	events := store.events["group:123"]
	if len(events) != 1 || events[0].MessageID != "101" || events[0].RawMessage != "机器人发出的原消息" {
		t.Fatalf("persisted outgoing event = %#v", events)
	}

	if err := runtime.HandleEvent(context.Background(), MessageEvent{
		Kind:       EventKindNotice,
		SubType:    "group_recall",
		Time:       200,
		SelfID:     "42",
		GroupID:    "123",
		UserID:     "42",
		OperatorID: "admin",
		MessageID:  "101",
	}); err != nil {
		t.Fatal(err)
	}
	events = store.events["group:123"]
	if len(events) != 2 {
		t.Fatalf("persisted events = %#v", events)
	}
	recall := events[1]
	if !isRecallNotice(recall) || recall.OperatorID != "admin" || recall.RawMessage != "机器人发出的原消息" {
		t.Fatalf("administrator recall = %#v", recall)
	}
}

type recallGetMsgChannel struct {
	nilChannel
	calls int
}

func (c *recallGetMsgChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.calls++
	if action != "get_msg" || stringFromAny(params["message_id"]) != "remote-1" {
		return nil, fmt.Errorf("unexpected call %s %#v", action, params)
	}
	return map[string]any{
		"time":         int64(150),
		"message_type": "group",
		"group_id":     "123",
		"user_id":      "20002",
		"message_id":   "remote-1",
		"raw_message":  "接入端补回的正文",
		"message": []any{
			map[string]any{"type": "text", "data": map[string]any{"text": "接入端补回的正文"}},
		},
		"sender": map[string]any{"user_id": "20002", "card": "Alice"},
	}, nil
}
