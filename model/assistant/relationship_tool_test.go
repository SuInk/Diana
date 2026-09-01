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
	// 主人的好感度现在也照常记录，但只由互动攒出来：自己填的数不叫记录，而且
	// 「给他加 5 分」被认成主人自己时正好在这里兜住。
	_, err = ownerTool.Run(context.Background(), map[string]any{"operation": "set", "target_user_id": "10001", "value": 0})
	if err == nil || !strings.Contains(err.Error(), "不能自己给自己设置") {
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

// 画像和好感度一样是群里公开的：普通成员查别人也拿得到。榜单不带画像是体积
// 考虑，不是权限——那条路对主人同样不带。
func TestDianaRelationshipToolSharesPortraitWithGroupMembers(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = UserMemoryProfile{
		UserID:       "10005",
		DisplayName:  "Alice",
		Favorability: 30,
		MessageCount: 40,
		Portrait: []UserPortraitTrait{
			{Field: PortraitFieldResidence, Label: "居住地点", Value: "住在杭州", Source: PortraitSourceStated},
		},
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {"group_id": 20002, "user_id": 10005, "nickname": "Alice", "role": "member"},
		"get_group_member_list": {"members": []any{
			map[string]any{"group_id": "20002", "user_id": "10005", "nickname": "Alice"},
		}},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)

	strangerEvent := MessageEvent{Kind: EventKindGroup, SelfID: "10000", GroupID: "20002", UserID: "10009"}
	raw, err := newDianaRelationshipTool(runtime, strangerEvent).Run(context.Background(), map[string]any{
		"operation": "get", "target_user_id": "10005",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || result.Target.Favorability != 30 {
		t.Fatalf("public relationship data should still be readable: %#v", result.Target)
	}
	if len(result.Target.Portrait) != 1 || !strings.Contains(raw, "住在杭州") {
		t.Fatalf("an ordinary member should be able to read another member's portrait: %s", raw)
	}

	// 榜单不带画像：一次最多 50 人，七栏画像会把结果撑爆，而排行用不到它。
	// 这条对主人同样成立，所以它不是权限。
	ownerEvent := MessageEvent{Kind: EventKindGroup, SelfID: "10000", GroupID: "20002", UserID: "10001"}
	listRaw, err := newDianaRelationshipTool(runtime, ownerEvent).Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listRaw, "住在杭州") {
		t.Fatalf("the ranking should stay statistics-only: %s", listRaw)
	}

	// 本人和主人自然也查得到。
	selfEvent := MessageEvent{Kind: EventKindGroup, SelfID: "10000", GroupID: "20002", UserID: "10005"}
	selfRaw, err := newDianaRelationshipTool(runtime, selfEvent).Run(context.Background(), map[string]any{"operation": "get"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selfRaw, "住在杭州") {
		t.Fatalf("the person themselves cannot read their own portrait: %s", selfRaw)
	}
	ownerRaw, err := newDianaRelationshipTool(runtime, ownerEvent).Run(context.Background(), map[string]any{
		"operation": "get", "target_user_id": "10005",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ownerRaw, "住在杭州") {
		t.Fatalf("bot owner cannot read a member portrait: %s", ownerRaw)
	}
}

func TestDianaRelationshipToolRecordsAndForgetsPortrait(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{Kind: EventKindPrivate, SelfID: "10000", UserID: "10005", SenderName: "Alice"}
	tool := newDianaRelationshipTool(runtime, event)

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "portrait_set", "portrait_field": "occupation", "portrait_value": "做后端开发",
	}); err != nil {
		t.Fatal(err)
	}
	stored := memory.profiles["10005"].Portrait
	if len(stored) != 1 || stored[0].Field != PortraitFieldOccupation || stored[0].Source != PortraitSourceManual {
		t.Fatalf("portrait was not recorded: %#v", stored)
	}
	// 当面记下的画像不算一次新的互动，互动次数不该被它顶上去。
	if memory.profiles["10005"].MessageCount != 0 {
		t.Fatalf("portrait write bumped the interaction count: %#v", memory.profiles["10005"])
	}

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "portrait_forget", "portrait_field": "occupation",
	}); err != nil {
		t.Fatal(err)
	}
	if len(memory.profiles["10005"].Portrait) != 0 {
		t.Fatalf("portrait was not cleared: %#v", memory.profiles["10005"].Portrait)
	}

	// 别人的画像只有主人能改。
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "portrait_set", "target_user_id": "10006", "portrait_field": "residence", "portrait_value": "住在上海",
	}); err == nil {
		t.Fatal("a member must not be able to edit someone else's portrait")
	}
	ownerTool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, SelfID: "10000", UserID: "10001"})
	if _, err := ownerTool.Run(context.Background(), map[string]any{
		"operation": "portrait_set", "target_user_id": "10006", "portrait_field": "residence", "portrait_value": "住在上海",
	}); err != nil {
		t.Fatal(err)
	}
	if len(memory.profiles["10006"].Portrait) != 1 {
		t.Fatalf("owner edit did not land: %#v", memory.profiles["10006"])
	}
}
