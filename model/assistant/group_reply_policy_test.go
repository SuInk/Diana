// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

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
		BotAccount:    "42",
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
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
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

func TestDianaOneBotGroupToolUpdatesReplyPolicyForBotOwner(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "10001", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	runtime.SetGroupConfigStore(store)
	tool := newDianaOneBotGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"})

	raw, err := tool.Run(context.Background(), map[string]any{
		"operation":                    "set_reply_policy",
		"proactive_reply_chance":       0.4,
		"proactive_reply_threshold":    0.93,
		"minimum_reply_member_level":   15,
		"natural_interjection_enabled": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaOneBotGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.OperatorRole != "bot_owner" || result.ReplyPolicy == nil || result.ReplyPolicy.MinimumReplyMemberLevel != 15 || !result.ReplyPolicy.NaturalInterjectionEnabled {
		t.Fatalf("result = %#v", result)
	}
	saved, ok := store.ConfigForGroup("123")
	if !ok || saved.ProactiveReplyChance != 0.4 || saved.ProactiveReplyThreshold != 0.93 || saved.MinimumReplyMemberLevel != 15 || saved.NaturalInterjectionEnabled == nil || !*saved.NaturalInterjectionEnabled {
		t.Fatalf("saved = %#v, ok = %v", saved, ok)
	}
}

func TestDianaOneBotGroupToolRejectsOrdinaryMemberReplyPolicyUpdate(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {"user_id": "10001", "role": "member", "level": "69"},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "900"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&testWritableGroupConfigStore{})
	tool := newDianaOneBotGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", SenderRole: "member"})

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
	if level, ok := parseOneBotGroupLevel(event.SenderLevel); !ok || level != 69 {
		t.Fatalf("parsed level = %d, ok = %v", level, ok)
	}
}

func TestDianaOneBotGroupToolSwitchesToCustomWhenChatInChanges(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{GroupID: "123", Enabled: true, EnabledSet: true, ResponseMode: ResponseModeStandard}, runtime.Config())
	runtime.SetGroupConfigStore(store)
	tool := newDianaOneBotGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"})

	raw, err := tool.Run(context.Background(), map[string]any{
		"operation":     "set_reply_policy",
		"chat_in_level": "max",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaOneBotGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	// 预设模式会在运行时套回自己的档位，所以必须同时切成自定义，改动才真的生效。
	saved, ok := store.ConfigForGroup("123")
	if !ok || saved.ResponseMode != ResponseModeCustom || saved.ChatInLevel != ChatInLevelMax {
		t.Fatalf("saved = %#v, ok = %v", saved, ok)
	}
	if result.ReplyPolicy == nil || result.ReplyPolicy.ChatInLevel != string(ChatInLevelMax) {
		t.Fatalf("reported policy = %#v", result.ReplyPolicy)
	}
	effective := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "123"})
	if effective.ChatInLevel != ChatInLevelMax {
		t.Fatalf("effective level = %q, want max", effective.ChatInLevel)
	}
}

func TestDianaOneBotGroupReplyPolicyReportsThePresetInsteadOfTheRawLevel(t *testing.T) {
	// 群仍是标准模式时，运行时用的是预设档位，报告也必须说预设值。
	policy := dianaOneBotGroupReplyPolicyFromConfig(GroupConfig{
		GroupID: "123", ResponseMode: ResponseModeStandard, ChatInLevel: ChatInLevelMax,
	})
	if policy.ChatInLevel != string(ChatInLevelLow) {
		t.Fatalf("reported level = %q, want the standard preset", policy.ChatInLevel)
	}
	custom := dianaOneBotGroupReplyPolicyFromConfig(GroupConfig{
		GroupID: "123", ResponseMode: ResponseModeCustom, ChatInLevel: ChatInLevelMax,
	})
	if custom.ChatInLevel != string(ChatInLevelMax) {
		t.Fatalf("custom reported level = %q, want max", custom.ChatInLevel)
	}
}
