// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestDianaOneBotGroupToolListsOtherMembersWithMentions(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {
			"items": []any{
				map[string]any{"group_id": "20001", "user_id": "10001", "nickname": "TestOwner"},
				map[string]any{"group_id": "20001", "user_id": "10002", "nickname": "Alice", "card": "Alice Card"},
				map[string]any{"group_id": "20001", "user_id": "20002", "nickname": "Alice"},
				map[string]any{"group_id": "20001", "user_id": "10000", "nickname": "Diana"},
			},
		},
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, channel, NewPluginManager(NewPlatformInterfacePlugin()), nil, nil, nil, nil)
	tool := newDianaGroupTool(runtime, MessageEvent{
		Kind:    EventKindGroup,
		SelfID:  "10000",
		GroupID: "20001",
		UserID:  "10001",
	})

	raw, err := tool.listMembers(context.Background(), map[string]any{
		"operation":              "members",
		"exclude_current_sender": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Members) != 2 || result.Members[0].UserID != "10002" || result.Members[1].UserID != "20002" {
		t.Fatalf("members = %#v", result.Members)
	}
	if result.Total != 2 || result.GroupTotal != 4 {
		t.Fatalf("counts = matched %d group %d", result.Total, result.GroupTotal)
	}
	if result.Members[0].Mention != "<diana-at id=\"10002\"/>" || result.Members[0].DisplayName != "Alice Card" {
		t.Fatalf("Alice = %#v", result.Members[0])
	}
}

func TestRuntimeAgentUsesOneBotGroupToolToMentionOtherMembers(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {
			"items": []any{
				map[string]any{"group_id": "20001", "user_id": "10001", "nickname": "TestOwner"},
				map[string]any{"group_id": "20001", "user_id": "10002", "nickname": "Alice"},
			},
		},
	}}
	provider := &privacyAwareTestProvider{}
	var targetAlias string
	provider.generate = func(call int, req llm.GenerateRequest) (string, error) {
		switch call {
		case 1:
			return `{"action":"none"}`, nil
		case 2:
			return `{"action":"tool","tool":"tools_load","input":{"names":["platform"]}}`, nil
		case 3:
			return `{"action":"tool","tool":"tools_execute","input":{"name":"platform","input":{"operation":"member_list"}}}`, nil
		case 4:
			match := regexp.MustCompile(`"user_id"\s*:\s*"(im_[a-z_]+_[0-9a-f]+)"\s*,\s*"display_name"\s*:\s*"Alice"`).FindStringSubmatch(requestTextForPrivacyTest(req))
			if len(match) > 1 {
				targetAlias = match[1]
			}
			if targetAlias == "" {
				return "", fmt.Errorf("Alice privacy alias missing from tool result")
			}
			return fmt.Sprintf(`{"action":"final","content":"[CQ:at,qq=%s] 喊你一下。"}`, targetAlias), nil
		default:
			return "", fmt.Errorf("unexpected LLM call %d", call)
		}
	}
	runtime := NewRuntime(BotConfig{
		BotAccount:   "10000",
		AgentEnabled: true,
	}, channel, NewPluginManager(NewPlatformInterfacePlugin()), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind:       EventKindGroup,
		SelfID:     "10000",
		GroupID:    "20001",
		UserID:     "10001",
		MessageID:  "ask-1",
		SenderName: "TestOwner",
		RawMessage: "Diana@下除了我以外的其余人",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana@下除了我以外的其余人"}}},
		ToMe:       true,
	}

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "[CQ:at,qq=10002]") {
		t.Fatalf("reply = %q", reply)
	}
	if len(provider.requests) != 4 || !requestMessagesContain(provider.requests[1].Messages, "platform") || !requestMessagesContain(provider.requests[3].Messages, targetAlias) {
		t.Fatalf("requests = %#v", provider.requests)
	}
	for _, req := range provider.requests {
		protected := requestTextForPrivacyTest(req)
		for _, realID := range []string{"10000", "20001", "10001", "10002"} {
			if strings.Contains(protected, realID) {
				t.Fatalf("provider request leaked QQ ID %s: %s", realID, protected)
			}
		}
	}
	segments := buildOutgoingSegments(channel.sent[0])
	atCount := 0
	for _, segment := range segments {
		if segment["type"] == "at" && segment["data"].(map[string]string)["qq"] == "10002" {
			atCount++
		}
	}
	if atCount != 1 {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestRuntimeAgentAnswersPromotedGroupCountFollowupWithOneBotGroupTool(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {
			"items": []any{
				map[string]any{"group_id": "20001", "user_id": "10001", "nickname": "Alice"},
				map[string]any{"group_id": "20001", "user_id": "10002", "nickname": "Bob"},
				map[string]any{"group_id": "20001", "user_id": "42", "nickname": "Diana"},
			},
		},
	}}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"tool","tool":"tools_load","input":{"names":["platform"]}}`,
		`{"action":"tool","tool":"tools_execute","input":{"name":"platform","input":{"operation":"member_list"}}}`,
		`{"action":"final","content":"群里现在有 3 个人。"}`,
		`{"send_confidence":0.99,"reason":"准确回答群成员数量"}`,
	}}
	runtime := NewRuntime(BotConfig{
		AgentEnabled: true,
		BotAccount:   "42",
	}, channel, NewPluginManager(NewPlatformInterfacePlugin()), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind:           EventKindGroup,
		SelfID:         "42",
		GroupID:        "20001",
		UserID:         "10001",
		MessageID:      "group-count-followup",
		SenderName:     "Alice",
		RawMessage:     "群里现在几个人",
		Segments:       []MessageSegment{{Type: "text", Data: map[string]string{"text": "群里现在几个人"}}},
		proactiveReply: true,
	}
	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "群里现在有 3 个人" || len(channel.sent) != 1 || channel.sent[0].Text != reply {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	if calls := channel.callsSnapshot(); len(calls) != 1 || calls[0].action != "get_group_member_list" {
		t.Fatalf("OneBot calls=%#v", calls)
	}
	if len(provider.requests) != 4 || !requestMessagesContain(provider.requests[2].Messages, "member_list") {
		t.Fatalf("provider requests=%#v", provider.requests)
	}
}

func TestDianaOneBotGroupToolSearchesByCardOrNickname(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_list": {
			"items": []any{
				map[string]any{"group_id": "123", "user_id": "10001", "nickname": "Alice", "card": "阿梨"},
				map[string]any{"group_id": "123", "user_id": "10002", "nickname": "Bob"},
			},
		},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(NewPlatformInterfacePlugin()), nil, nil, nil, nil)
	raw, err := newDianaGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "owner"}).listMembers(context.Background(), map[string]any{
		"operation": "members",
		"query":     "阿梨",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"user_id":"10001"`) || strings.Contains(raw, `"user_id":"10002"`) {
		t.Fatalf("raw = %s", raw)
	}
}

// members 不设条数上限：匹配人数如实报，按整条分页，offset 续读拿到剩下的。
func TestDianaOneBotGroupToolMembersPagesWithoutCap(t *testing.T) {
	const total = 130
	items := make([]any, 0, total)
	for index := 0; index < total; index++ {
		items = append(items, map[string]any{"group_id": "20001", "user_id": fmt.Sprintf("%d", 30000+index), "nickname": fmt.Sprintf("成员%d", index)})
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{"get_group_member_list": {"items": items}}}
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, channel, NewPluginManager(NewPlatformInterfacePlugin()), nil, nil, nil, nil)
	tool := newDianaGroupTool(runtime, MessageEvent{Kind: EventKindGroup, SelfID: "10000", GroupID: "20001", UserID: "10001"})
	list := func(input map[string]any) dianaGroupResult {
		t.Helper()
		raw, err := tool.listMembers(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		var result dianaGroupResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return result
	}
	first := list(map[string]any{"operation": "members"})
	if first.Total != total || len(first.Members) != defaultGroupMemberLimit || first.Page == nil || !first.Page.HasMore || first.Page.NextOffset != defaultGroupMemberLimit {
		t.Fatalf("first page = total %d members %d page %+v", first.Total, len(first.Members), first.Page)
	}
	rest := list(map[string]any{"operation": "members", "limit": 1000, "offset": first.Page.NextOffset})
	if len(rest.Members) != total-defaultGroupMemberLimit || rest.Page == nil || rest.Page.HasMore || rest.Members[len(rest.Members)-1].UserID != fmt.Sprintf("%d", 30000+total-1) {
		t.Fatalf("rest page = members %d page %+v", len(rest.Members), rest.Page)
	}
}

func groupMembersTestTool(t *testing.T, members []any) (*dianaGroupTool, *Runtime) {
	t.Helper()
	channel := &recordingChannel{apiResponses: map[string]map[string]any{"get_group_member_list": {"items": members}}}
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, channel, NewPluginManager(NewPlatformInterfacePlugin()), nil, nil, nil, nil)
	return newDianaGroupTool(runtime, MessageEvent{Kind: EventKindGroup, SelfID: "10000", GroupID: "20001", UserID: "10001"}), runtime
}

// 单次上限装不下时按整条切页：每页都在上限内，名片给全，offset 续读拿齐全部。
func TestDianaOneBotGroupToolMembersSplitByOutputBudget(t *testing.T) {
	const total = 40
	card := strings.Repeat("长名片", 40)
	items := make([]any, 0, total)
	for index := range total {
		items = append(items, map[string]any{"group_id": "20001", "user_id": fmt.Sprintf("%d", 30000+index), "nickname": "成员", "card": card})
	}
	tool, _ := groupMembersTestTool(t, items)
	ctx := agent.WithToolOutputBudget(context.Background(), 3000)
	seen := map[string]bool{}
	offset := 0
	for page := 0; ; page++ {
		if page > total {
			t.Fatal("分页没有前进")
		}
		raw, err := tool.listMembers(ctx, map[string]any{"operation": "members", "limit": 1000, "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		if size := len([]rune(raw)); size > 3000 {
			t.Fatalf("第 %d 页 %d 字超过上限", page, size)
		}
		var result dianaGroupResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		if result.Page == nil || len(result.Members) == 0 || result.Total != total {
			t.Fatalf("page = %+v members %d", result.Page, len(result.Members))
		}
		for _, member := range result.Members {
			if seen[member.UserID] || member.Card != card {
				t.Fatalf("重复或名片被截短：%+v", member)
			}
			seen[member.UserID] = true
		}
		if !result.Page.HasMore {
			break
		}
		if page == 0 && !strings.Contains(result.Page.PageNote, "单次结果上限") {
			t.Fatalf("第一页应写明按上限切开：%q", result.Page.PageNote)
		}
		offset = result.Page.NextOffset
	}
	if len(seen) != total {
		t.Fatalf("只拿到 %d 人，应为 %d", len(seen), total)
	}
}

// 按旧昵称找人时发言人名录按账号一页页翻完，不只取第一页，也不一次全量拉回。
func TestDianaOneBotGroupToolHistoricalNamesPageThroughDirectory(t *testing.T) {
	const total = 450
	members := make([]any, 0, total)
	speakers := make([]MessageSpeaker, 0, total)
	for index := range total {
		id := fmt.Sprintf("%d", 30000+index)
		members = append(members, map[string]any{"group_id": "20001", "user_id": id, "nickname": "现名" + id})
		speakers = append(speakers, MessageSpeaker{UserID: id, Names: []string{"现名" + id, "旧名" + id}})
	}
	tool, runtime := groupMembersTestTool(t, members)
	store := &speakerHistoryStore{speakers: speakers}
	runtime.SetMessageHistoryStore(store)
	raw, err := tool.listMembers(context.Background(), map[string]any{"operation": "members", "query": "旧名", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != total || result.Page == nil || !result.Page.HasMore {
		t.Fatalf("按旧昵称应匹配全部 %d 人：total=%d page=%+v", total, result.Total, result.Page)
	}
	if want := total/groupSpeakerPageSize + 1; store.speakerCalls != want {
		t.Fatalf("名录应分 %d 页取，实际 %d 次", want, store.speakerCalls)
	}
}
