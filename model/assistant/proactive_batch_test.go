// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestProactiveReplyBatchRoutesOnceAndSelectsTarget(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","turn_message_ids":["message-1"],"directed_at_bot":false,"answerable":true}`,
	}}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	candidates := []proactiveReplyCandidate{
		{
			Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1", SenderName: "Alice"},
			Text:  "这个报错应该怎么处理",
		},
		{
			Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2", MessageID: "message-2", SenderName: "Bob"},
			Text:  "我先去吃饭了",
		},
	}

	event, text, turn, allowed := runtime.routeProactiveReplyBatch(context.Background(), candidates)
	if !allowed {
		t.Fatal("batch route should allow the selected question")
	}
	if event.MessageID != "message-1" || text != "这个报错应该怎么处理" {
		t.Fatalf("selected event = %q text = %q", event.MessageID, text)
	}
	if len(turn) != 1 || turn[0].Event.MessageID != "message-1" {
		t.Fatalf("selected turn = %#v, want only message-1", turn)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("router calls = %d, want 1", len(provider.requests))
	}
	requestText := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	// 评分喂的是对话稿：两条候选按发送者和正文各占一行，不带账号。
	for _, want := range []string{`from="Alice" burst="1">这个报错应该怎么处理</msg>`, `<msg age="刚刚" from="Bob" current="true">我先去吃饭了</msg>`} {
		if !strings.Contains(requestText, want) {
			t.Fatalf("batch transcript missing %s: %s", want, requestText)
		}
	}
	if strings.Contains(requestText, "user-1") || strings.Contains(requestText, "user-2") {
		t.Fatalf("batch payload leaked candidate identities: %s", requestText)
	}
	routePrompt := provider.requests[0].Messages[0].Content
	for _, want := range []string{"只评估 current=\"true\" 的那一条", "前面的对话供理解上下文", "不选择其他消息作为回复目标", "同一内容已经回答"} {
		if !strings.Contains(routePrompt, want) {
			t.Fatalf("batch route prompt missing %q: %s", want, routePrompt)
		}
	}
}

func TestProactiveReplyBatchReplacesLegacyRouterPrompt(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","turn_message_ids":["message-1"],"directed_at_bot":false,"answerable":true}`,
	}}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
		PromptOverrides:         PromptOverrides{promptLegacyRouterSpec.Key: "custom proactive router prompt"},
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	candidate := proactiveReplyCandidate{
		Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1"},
		Text:  "这个报错应该怎么处理",
	}

	_, _, _, allowed := runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{candidate})
	if !allowed {
		t.Fatal("configured proactive router should allow the selected question")
	}
	if len(provider.requests) != 1 || len(provider.requests[0].Messages) == 0 {
		t.Fatalf("router requests = %#v", provider.requests)
	}
	if got := provider.requests[0].Messages[0].Content; strings.Contains(got, "custom proactive router prompt") || !strings.Contains(got, "闲聊档位：low") {
		t.Fatalf("router prompt = %q", got)
	}
}

func TestProactiveReplyRouterTimeoutKeepsQuestionsSilent(t *testing.T) {
	// 路由超时以前会退回一条词表规则：扫到问号或「怎么/为什么」就当成公开问题强行
	// 回答。那是拿关键词判断语义意图，而且判错的方向是「本来不该说话却开口」。
	// 没有模型结论时保持沉默才是保守的默认值。
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return failingLLMProvider{err: context.DeadlineExceeded}, nil
	})
	candidate := proactiveReplyCandidate{
		Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "question-1"},
		Text:  "这个报错应该怎么处理？",
	}
	event, _, _, allowed := runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{candidate})
	if allowed {
		t.Fatalf("timed-out routing still produced a reply: %#v", event)
	}
	if !strings.Contains(event.routingReason, "保持沉默") {
		t.Fatalf("routing reason = %q", event.routingReason)
	}
}

func TestProactiveReplyRouterTimeoutKeepsStatementSilent(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return failingLLMProvider{err: context.DeadlineExceeded}, nil
	})
	candidate := proactiveReplyCandidate{
		Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "statement-1"},
		Text:  "我先去吃饭了",
	}
	_, _, _, allowed := runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{candidate})
	if allowed {
		t.Fatal("plain statement should not bypass timed-out semantic routing")
	}
}

func TestProactiveReplyBatchSelectsCompleteSemanticTurn(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":true,"confidence":0.99,"category":"bot_related","target_message_id":"message-3","turn_message_ids":["message-1","message-2","message-3"],"directed_at_bot":true,"answerable":true}`,
	}}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.9,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	candidates := []proactiveReplyCandidate{
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1"}, Text: "1+1"},
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-2"}, Text: "5+6"},
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-3"}, Text: "4+8"},
	}

	event, text, turn, allowed := runtime.routeProactiveReplyBatch(context.Background(), candidates)
	if !allowed || event.MessageID != "message-3" || text != "4+8" {
		t.Fatalf("route = event %q text %q allowed %v", event.MessageID, text, allowed)
	}
	if len(turn) != 3 {
		t.Fatalf("turn = %#v, want all three messages", turn)
	}
	for index, want := range []string{"message-1", "message-2", "message-3"} {
		if turn[index].Event.MessageID != want {
			t.Fatalf("turn[%d] = %q, want %q", index, turn[index].Event.MessageID, want)
		}
	}
}
