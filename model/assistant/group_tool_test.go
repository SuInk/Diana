// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, channel, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaOneBotGroupTool(runtime, MessageEvent{
		Kind:    EventKindGroup,
		SelfID:  "10000",
		GroupID: "20001",
		UserID:  "10001",
	})

	raw, err := tool.Run(context.Background(), map[string]any{
		"operation":              "members",
		"exclude_current_sender": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaOneBotGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Members) != 2 || result.Members[0].UserID != "10002" || result.Members[1].UserID != "20002" {
		t.Fatalf("members = %#v", result.Members)
	}
	if result.Total != 2 || result.GroupTotal != 4 {
		t.Fatalf("counts = matched %d group %d", result.Total, result.GroupTotal)
	}
	if result.Members[0].MentionCQ != "[CQ:at,qq=10002]" || result.Members[0].DisplayName != "Alice Card" {
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
			return `{"action":"tool","tool":"diana.onebot_group","input":{"operation":"members","exclude_current_sender":true}}`, nil
		case 3:
			targetAlias = privacyAliasForDisplayName(req, "Alice")
			if targetAlias == "" {
				return "", fmt.Errorf("Alice privacy alias missing from tool result")
			}
			return fmt.Sprintf(`{"action":"final","content":"[CQ:at,qq=%s] 喊你一下。"}`, targetAlias), nil
		default:
			return "", fmt.Errorf("unexpected LLM call %d", call)
		}
	}
	runtime := NewRuntime(BotConfig{
		BotAccount:    "10000",
		AgentEnabled:  true,
		AgentWorkDir:  t.TempDir(),
		AgentMaxSteps: 3,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
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
	if len(provider.requests) != 3 || !requestMessagesContain(provider.requests[1].Messages, "diana.onebot_group") || !requestMessagesContain(provider.requests[2].Messages, `"mention_cq": "[CQ:at,qq=`+targetAlias+`]"`) {
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
		`{"action":"tool","tool":"diana.onebot_group","input":{"operation":"members"}}`,
		`{"action":"final","content":"群里现在有 3 个人。"}`,
		`{"should_send":true,"confidence":0.99,"reason":"准确回答群成员数量"}`,
	}}
	runtime := NewRuntime(BotConfig{
		AgentEnabled:  true,
		AgentWorkDir:  t.TempDir(),
		AgentMaxSteps: 3,
		BotAccount:    "42",
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
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
	if reply != "群里现在有 3 个人。" || len(channel.sent) != 1 || channel.sent[0].Text != reply {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	if calls := channel.callsSnapshot(); len(calls) != 1 || calls[0].action != "get_group_member_list" {
		t.Fatalf("OneBot calls=%#v", calls)
	}
	if len(provider.requests) != 3 || !requestMessagesContain(provider.requests[1].Messages, `"group_total": 3`) {
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
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	raw, err := newDianaOneBotGroupTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "owner"}).Run(context.Background(), map[string]any{
		"operation": "members",
		"query":     "阿梨",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"user_id": "10001"`) || strings.Contains(raw, `"user_id": "10002"`) {
		t.Fatalf("raw = %s", raw)
	}
}
