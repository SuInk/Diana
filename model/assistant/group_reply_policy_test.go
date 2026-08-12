package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type testWritableGroupConfigStore struct {
	set GroupConfigSet
}

func (s *testWritableGroupConfigStore) ConfigForGroup(groupID string) (GroupConfig, bool) {
	return s.set.ConfigForGroup(groupID)
}

func (s *testWritableGroupConfigStore) SaveGroupConfig(cfg GroupConfig, base BotConfig) (GroupConfig, error) {
	s.set = s.set.Upsert(cfg, base)
	saved, _ := s.set.ConfigForGroup(cfg.GroupID)
	return saved, nil
}

func TestGroupConfigOverridesProactivePolicy(t *testing.T) {
	base := BotConfig{ProactiveReplyChance: 0.8, ProactiveReplyThreshold: 0.85}
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:                 "123",
		Enabled:                 true,
		EnabledSet:              true,
		ProactiveReplyChance:    0.35,
		ProactiveReplyThreshold: 0.94,
		MinimumReplyMemberLevel: 12,
	}, base)
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(store)

	effective := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "123"})
	if effective.ProactiveReplyChance != 0.35 || effective.ProactiveReplyThreshold != 0.94 {
		t.Fatalf("effective proactive policy = chance %v threshold %v", effective.ProactiveReplyChance, effective.ProactiveReplyThreshold)
	}
	group, ok := runtime.groupConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "123"})
	if !ok || group.MinimumReplyMemberLevel != 12 {
		t.Fatalf("group config = %#v, ok = %v", group, ok)
	}
}

func TestRuntimeIgnoresLowLevelMemberBeforeReplyDecisionButKeepsHistory(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		BotQQ:         "42",
		OwnerID:       "900",
		GroupTriggers: []string{"Diana"},
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:                 "123",
		Enabled:                 true,
		EnabledSet:              true,
		MinimumReplyMemberLevel: 10,
	}, runtime.Config())
	runtime.SetGroupConfigStore(store)
	historyStore := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(historyStore)
	event := MessageEvent{
		Kind:        EventKindGroup,
		SelfID:      "42",
		GroupID:     "123",
		UserID:      "10001",
		MessageID:   "low-1",
		RawMessage:  "Diana 帮我看看",
		Segments:    []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 帮我看看"}}},
		SenderRole:  "member",
		SenderLevel: 9,
	}

	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	if handled || outcome != "ignored_member_level" {
		t.Fatalf("handled = %v, outcome = %q", handled, outcome)
	}
	history := runtime.contextHistory(event)
	if len(history) != 1 || history[0].MessageID != "low-1" {
		t.Fatalf("runtime history = %#v", history)
	}
	persisted := historyStore.events[sessionKey(event)]
	if len(persisted) != 1 || persisted[0].MessageID != "low-1" {
		t.Fatalf("persisted history = %#v", persisted)
	}
}

func TestRuntimeAllowsLowLevelMemberWhenDirectlyMentioned(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:                 "123",
		Enabled:                 true,
		EnabledSet:              true,
		MinimumReplyMemberLevel: 50,
	}, runtime.Config())
	runtime.SetGroupConfigStore(store)
	event := MessageEvent{
		Kind:        EventKindGroup,
		SelfID:      "42",
		GroupID:     "123",
		UserID:      "10001",
		MessageID:   "mention-1",
		RawMessage:  "[CQ:at,qq=42] 帮我看看",
		Segments:    []MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}, {Type: "text", Data: map[string]string{"text": "帮我看看"}}},
		SenderRole:  "member",
		SenderLevel: 1,
		ToMe:        true,
	}

	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	if !handled || outcome != "replied" {
		t.Fatalf("handled = %v, outcome = %q", handled, outcome)
	}
}

func TestRuntimeAllowsLowLevelGroupAdministrator(t *testing.T) {
	runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:                 "123",
		Enabled:                 true,
		EnabledSet:              true,
		MinimumReplyMemberLevel: 50,
	}, runtime.Config())
	runtime.SetGroupConfigStore(store)
	event := MessageEvent{
		Kind:        EventKindGroup,
		GroupID:     "123",
		UserID:      "10001",
		MessageID:   "admin-1",
		RawMessage:  "Diana 在吗",
		SenderRole:  "admin",
		SenderLevel: 1,
	}

	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	if !handled || outcome != "replied" {
		t.Fatalf("handled = %v, outcome = %q", handled, outcome)
	}
}

func TestRuntimeFallsBackToNapCatWhenSenderLevelIsMissing(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {"user_id": "10001", "role": "member", "level": "9"},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:                 "123",
		Enabled:                 true,
		EnabledSet:              true,
		MinimumReplyMemberLevel: 10,
	}, runtime.Config())
	runtime.SetGroupConfigStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "lookup-1"}

	ignored, decision := runtime.shouldIgnoreGroupReplyByMemberLevel(context.Background(), event)
	if !ignored || !decision.LevelSet || decision.Level != 9 || decision.Reason != "member_level_below_minimum" {
		t.Fatalf("ignored = %v, decision = %#v", ignored, decision)
	}
	if len(channel.calls) != 1 || channel.calls[0].action != "get_group_member_info" {
		t.Fatalf("NapCat calls = %#v", channel.calls)
	}
}

func TestDianaQQGroupToolUpdatesReplyPolicyForBotOwner(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "10001", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	runtime.SetGroupConfigStore(store)
	tool := newDianaQQGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"})

	raw, err := tool.Run(context.Background(), map[string]any{
		"operation":                  "set_reply_policy",
		"proactive_reply_chance":     0.4,
		"proactive_reply_threshold":  0.93,
		"minimum_reply_member_level": 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaQQGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.OperatorRole != "bot_owner" || result.ReplyPolicy == nil || result.ReplyPolicy.MinimumReplyMemberLevel != 15 {
		t.Fatalf("result = %#v", result)
	}
	saved, ok := store.ConfigForGroup("123")
	if !ok || saved.ProactiveReplyChance != 0.4 || saved.ProactiveReplyThreshold != 0.93 || saved.MinimumReplyMemberLevel != 15 {
		t.Fatalf("saved = %#v, ok = %v", saved, ok)
	}
}

func TestDianaQQGroupToolAcceptsLegacyProactivePolicyNames(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	runtime.SetGroupConfigStore(store)
	tool := newDianaQQGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"})

	_, err := tool.Run(context.Background(), map[string]any{
		"operation":               "set_reply_policy",
		"passive_reply_chance":    0.45,
		"passive_reply_threshold": 0.92,
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := store.ConfigForGroup("123")
	if !ok || saved.ProactiveReplyChance != 0.45 || saved.ProactiveReplyThreshold != 0.92 {
		t.Fatalf("saved = %#v, ok = %v", saved, ok)
	}
}

func TestDianaQQGroupToolPrefersProactivePolicyNames(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	runtime.SetGroupConfigStore(store)
	tool := newDianaQQGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"})

	_, err := tool.Run(context.Background(), map[string]any{
		"operation":                 "set_reply_policy",
		"proactive_reply_chance":    0.75,
		"proactive_reply_threshold": 0.96,
		"passive_reply_chance":      0.25,
		"passive_reply_threshold":   0.6,
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := store.ConfigForGroup("123")
	if !ok || saved.ProactiveReplyChance != 0.75 || saved.ProactiveReplyThreshold != 0.96 {
		t.Fatalf("saved = %#v, ok = %v", saved, ok)
	}
}

func TestDianaQQGroupToolRejectsOrdinaryMemberReplyPolicyUpdate(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {"user_id": "10001", "role": "member", "level": "69"},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "900"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&testWritableGroupConfigStore{})
	tool := newDianaQQGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", SenderRole: "member"})

	_, err := tool.Run(context.Background(), map[string]any{
		"operation":                  "set_reply_policy",
		"minimum_reply_member_level": 20,
	})
	if err == nil || !strings.Contains(err.Error(), "只有机器人主人、群主或群管理员") {
		t.Fatalf("error = %v", err)
	}
}

func TestMessageEventFromEnvelopeKeepsSenderRoleAndLevel(t *testing.T) {
	var envelope oneBotEnvelope
	if err := json.Unmarshal([]byte(`{"post_type":"message","message_type":"group","group_id":123,"user_id":10001,"message":[{"type":"text","data":{"text":"hello"}}],"sender":{"nickname":"Alice","role":"admin","level":"LV69"}}`), &envelope); err != nil {
		t.Fatal(err)
	}
	event := messageEventFromEnvelope(envelope)
	if event.SenderRole != "admin" || event.SenderLevel != 69 || event.SenderLevelLabel != "LV69" {
		t.Fatalf("event sender policy fields = role %q level %d label %q", event.SenderRole, event.SenderLevel, event.SenderLevelLabel)
	}
	if level, ok := parseQQGroupLevel(event.SenderLevel); !ok || level != 69 {
		t.Fatalf("parsed level = %d, ok = %v", level, ok)
	}
}
