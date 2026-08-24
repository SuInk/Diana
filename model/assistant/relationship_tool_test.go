// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDianaRelationshipToolUsesMentionedMemberAsTarget(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = UserMemoryProfile{
		UserID:       "10005",
		DisplayName:  "Alice",
		Favorability: 5,
		MessageCount: 18,
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {
			"group_id": 20002,
			"user_id":  10005,
			"nickname": "Alice",
			"role":     "member",
		},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:    EventKindGroup,
		SelfID:  "10000",
		UserID:  "10001",
		GroupID: "20002",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "Diana看下"}},
			{Type: "at", Data: map[string]string{"qq": "10005"}},
			{Type: "text", Data: map[string]string{"text": " 的好感度"}},
		},
	}

	raw, err := newDianaRelationshipTool(runtime, event).Run(context.Background(), map[string]any{"operation": "get"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || result.Target.UserID != "10005" || result.Target.DisplayName != "Alice" {
		t.Fatalf("target = %#v", result.Target)
	}
	if result.Target.Favorability != 5 || result.Target.MessageCount != 18 || result.Target.RelationshipTier != RelationshipAcquaintance {
		t.Fatalf("relationship = %#v", result.Target)
	}
	// 结果里不再带能力清单：它对所有等级都一样，留着只会被复述成本等级的特权。
	if result.Target.ScheduleLimit != 3 || !result.Target.CanGenerateImage || !result.Target.CanEditImage || !result.Target.CanDocumentOCR {
		t.Fatalf("permissions = %#v", result.Target)
	}
	if result.Target.Mention != "[diana-at:10005]" || !result.Target.HasHistory {
		t.Fatalf("mention/history = %#v", result.Target)
	}
}

func TestDianaRelationshipToolListsCurrentGroupForOwner(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10001"] = UserMemoryProfile{UserID: "10001", DisplayName: "Alice", Favorability: 20, MessageCount: 12}
	memory.profiles["10002"] = UserMemoryProfile{UserID: "10002", DisplayName: "Bob", Favorability: 80, MessageCount: 50}
	memory.profiles["10003"] = UserMemoryProfile{UserID: "10003", DisplayName: "Carol", Favorability: 5, MessageCount: 2}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {
			"members": []any{
				map[string]any{"group_id": "123", "user_id": "10001", "nickname": "Alice"},
				map[string]any{"group_id": "123", "user_id": "10002", "nickname": "Bob"},
				map[string]any{"group_id": "123", "user_id": "10003", "nickname": "Carol"},
			},
		},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	tool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "owner"})

	raw, err := tool.Run(context.Background(), map[string]any{"operation": "list", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].UserID != "10002" || result.Items[1].UserID != "10001" {
		t.Fatalf("items = %#v", result.Items)
	}

	// 榜单是群内公开互动的统计，普通成员同样可以查询。
	memberTool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10003"})
	raw, err = memberTool.Run(context.Background(), map[string]any{"operation": "list", "limit": 3})
	if err != nil {
		t.Fatalf("ordinary member should be able to list: %v", err)
	}
	var memberResult dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &memberResult); err != nil {
		t.Fatal(err)
	}
	if len(memberResult.Items) != 3 || memberResult.Items[0].UserID != "10002" {
		t.Fatalf("member items = %#v", memberResult.Items)
	}
}

func TestDianaRelationshipOwnerCanSetAndAdjustOthersFavorability(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10002"] = UserMemoryProfile{UserID: "10002", Favorability: 5, MessageCount: 18}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	ownerTool := newDianaRelationshipTool(runtime, MessageEvent{UserID: "10001"})

	raw, err := ownerTool.Run(context.Background(), map[string]any{"operation": "set", "target_user_id": "10002", "value": 80, "reason": "补录活动奖励"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || result.Target.Favorability != 80 || result.Target.MessageCount != 18 || memory.profiles["10002"].MessageCount != 18 {
		t.Fatalf("set result=%#v profile=%#v", result, memory.profiles["10002"])
	}
	if len(result.Target.RecentChanges) != 1 || result.Target.RecentChanges[0].Delta != 75 || result.Target.RecentChanges[0].Source != "owner_set" || result.Target.RecentChanges[0].Reason != "补录活动奖励" || result.Target.RecentChanges[0].OperatorID != "10001" {
		t.Fatalf("set recent changes=%#v", result.Target.RecentChanges)
	}
	raw, err = ownerTool.Run(context.Background(), map[string]any{"operation": "adjust", "target_user_id": "10002", "delta": -10})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || result.Target.Favorability != 70 || result.Target.MessageCount != 18 {
		t.Fatalf("adjust result=%#v", result)
	}
	if len(result.Target.RecentChanges) != 2 || result.Target.RecentChanges[0].Delta != -10 || result.Target.RecentChanges[0].Before != 80 || result.Target.RecentChanges[0].After != 70 || result.Target.RecentChanges[0].Source != "owner_adjust" {
		t.Fatalf("adjust recent changes=%#v", result.Target.RecentChanges)
	}

	_, err = newDianaRelationshipTool(runtime, MessageEvent{UserID: "10002"}).Run(context.Background(), map[string]any{"operation": "set", "target_user_id": "10003", "value": 100})
	if err == nil || !strings.Contains(err.Error(), "只有主人") {
		t.Fatalf("non-owner error=%v", err)
	}
	_, err = ownerTool.Run(context.Background(), map[string]any{"operation": "set", "target_user_id": "10001", "value": 0})
	if err == nil || !strings.Contains(err.Error(), "不能修改主人") {
		t.Fatalf("owner self error=%v", err)
	}
}

func TestRuntimeAgentQueriesMentionedUsersRelationship(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = UserMemoryProfile{
		UserID:       "10005",
		DisplayName:  "Alice",
		Favorability: 5,
		MessageCount: 18,
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {
			"group_id": 20002,
			"user_id":  10005,
			"nickname": "Alice",
		},
	}}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		`{"action":"tool","tool":"diana.relationship","input":{"operation":"get"}}`,
		`{"action":"final","content":"[CQ:at,qq=10005] 当前好感度是 5，关系等级是初识，互动 18 次。当前权限：基础聊天、媒体理解、网页搜索和 1 个提醒或订阅额度。"}`,
	}}
	runtime := NewRuntime(BotConfig{
		OwnerID:       "10001",
		BotAccount:    "10000",
		AgentEnabled:  true,
		AgentMaxSteps: 3,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:      EventKindGroup,
		SelfID:    "10000",
		UserID:    "10001",
		GroupID:   "20002",
		MessageID: "30004",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "Diana看下"}},
			{Type: "at", Data: map[string]string{"qq": "10005"}},
			{Type: "text", Data: map[string]string{"text": " 的好感度"}},
		},
	}

	reply, err := runtime.replyTo(context.Background(), event, PlainText(event.Segments))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "[CQ:at,qq=10005]") || !strings.Contains(reply, "好感度是 5") || !strings.Contains(reply, "当前权限") {
		t.Fatalf("reply = %q", reply)
	}
	if len(provider.requests) != 3 || !requestMessagesContain(provider.requests[2].Messages, `"favorability": 5`) {
		t.Fatalf("requests = %#v", provider.requests)
	}
	for _, want := range []string{"必须调用 diana.relationship", "operation=list", "不得以隐私", "不得编造"} {
		if !requestMessagesContain(provider.requests[1].Messages, want) {
			t.Fatalf("relationship guidance missing %q: %#v", want, provider.requests[1].Messages)
		}
	}
	// 提示词不再要求把工具结果逐字段念出来——那正是回复读起来像填表的原因。
	for _, unwanted := range []string{"最终回复必须同时说明", "不得省略工具结果中的 permissions", "必须简明列出结果中的 permissions"} {
		if requestMessagesContain(provider.requests[1].Messages, unwanted) {
			t.Fatalf("relationship guidance still mandates a field dump (%q)", unwanted)
		}
	}
}
