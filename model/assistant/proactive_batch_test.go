// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
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
	for _, want := range []string{`"message_id":"message-1"`, `"user_id":"user-1"`, `"message_id":"message-2"`, `"user_id":"user-2"`} {
		if !strings.Contains(requestText, want) {
			t.Fatalf("batch payload missing %s: %s", want, requestText)
		}
	}
	routePrompt := provider.requests[0].Messages[0].Content
	for _, want := range []string{"最近 15 秒内最多 3 条候选", "不能仅凭同一发送者或时间相邻就合并", "turn_message_ids", "连续补充的多个问题", "禁止换一种说法重复回答"} {
		if !strings.Contains(routePrompt, want) {
			t.Fatalf("batch route prompt missing %q: %s", want, routePrompt)
		}
	}
}

func TestProactiveReplyBatchUsesConfiguredRouterPrompt(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","turn_message_ids":["message-1"],"directed_at_bot":false,"answerable":true}`,
	}}
	runtime := NewRuntime(BotConfig{
		BotAccount:                 "42",
		ProactiveReplyChance:       1,
		ProactiveReplyThreshold:    0.8,
		ProactiveReplyRouterPrompt: "custom proactive router prompt",
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
	if got := provider.requests[0].Messages[0].Content; !strings.Contains(got, "custom proactive router prompt") {
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

func TestProactiveReplyTurnCombinesThreeMessagesIntoOneReply(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
		"1+1=2，5+6=11，4+8=12。",
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		AgentEnabled:   false,
		RequestTimeout: time.Minute,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	events := []MessageEvent{
		{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1", Time: 100, SenderName: "Alice", RawMessage: "1+1", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "1+1"}}}},
		{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-2", Time: 104, SenderName: "Alice", RawMessage: "5+6", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "5+6"}}}},
		{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-3", Time: 106, SenderName: "Alice", RawMessage: "4+8", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "4+8"}}}},
	}
	turn := make([]proactiveReplyCandidate, 0, len(events))
	for _, event := range events {
		turn = append(turn, proactiveReplyCandidate{Event: event, Text: event.RawMessage})
	}
	ctx := withProactiveReplyTurnContext(context.Background(), turn)
	reply, err := runtime.replyTo(ctx, events[2], events[2].RawMessage)
	if err != nil {
		t.Fatalf("replyTo() error = %v", err)
	}
	if reply != "1+1=2，5+6=11，4+8=12" {
		t.Fatalf("reply = %q", reply)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != reply {
		t.Fatalf("sent = %#v, want one combined QQ message", channel.sent)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("provider calls = %d, want intent route, final reply, and quality review", len(provider.requests))
	}
	finalRequest := provider.requests[len(provider.requests)-2]
	joined := make([]string, 0, len(finalRequest.Messages))
	for _, message := range finalRequest.Messages {
		joined = append(joined, message.Content)
	}
	prompt := strings.Join(joined, "\n")
	for _, want := range []string{"覆盖这一轮里的全部实质问题", "【当前同轮补充消息", "1+1", "5+6", "【当前需要回复的消息】", "4+8"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("final prompt missing %q: %s", want, prompt)
		}
	}
}

func TestProactiveReplyDecisionCandidatesUsesBoundedRecentWindow(t *testing.T) {
	base := time.Now()
	items := []proactiveReplyCandidate{
		{Event: MessageEvent{MessageID: "message-1"}, QueuedAt: base.Add(-30 * time.Second)},
		{Event: MessageEvent{MessageID: "message-2"}, QueuedAt: base.Add(-12 * time.Second)},
		{Event: MessageEvent{MessageID: "message-3"}, QueuedAt: base.Add(-8 * time.Second)},
		{Event: MessageEvent{MessageID: "message-4"}, QueuedAt: base.Add(-4 * time.Second)},
		{Event: MessageEvent{MessageID: "message-5"}, QueuedAt: base},
	}

	got := proactiveReplyDecisionCandidates(items)
	if len(got) != 3 {
		t.Fatalf("bounded candidates = %d, want 3: %#v", len(got), got)
	}
	for i, want := range []string{"message-3", "message-4", "message-5"} {
		if got[i].Event.MessageID != want {
			t.Fatalf("candidate %d = %q, want %q", i, got[i].Event.MessageID, want)
		}
	}

	windowed := proactiveReplyDecisionCandidates([]proactiveReplyCandidate{
		{Event: MessageEvent{MessageID: "old"}, QueuedAt: base.Add(-20 * time.Second)},
		{Event: MessageEvent{MessageID: "recent"}, QueuedAt: base},
	})
	if len(windowed) != 1 || windowed[0].Event.MessageID != "recent" {
		t.Fatalf("windowed candidates = %#v, want only recent", windowed)
	}
}

func TestProactiveReplyBatchReroutesOnceBeforeSending(t *testing.T) {
	channel := &recordingChannel{}
	first := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "message-1",
		Time:       100,
		SenderName: "Alice",
		RawMessage: "这不是 MyGO 里面的吗",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这不是 MyGO 里面的吗"}}},
	}
	second := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "message-2",
		Time:       105,
		SenderName: "Alice",
		RawMessage: "[图片]",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"url": "data:image/jpeg;base64,aGVsbG8="}}},
	}
	provider := &proactiveReplyRerouteProvider{second: second}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		OwnerID:                 "owner",
		AgentEnabled:            false,
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
		// 这条用例看的是合并后的回复指向哪条消息，所以要显式打开引用；
		// 默认档是 auto，运行时不补装饰件，读不到这个信号。
		ReplyReferenceMode: ReplyDecorationOn,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	provider.runtime = runtime
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	runtime.mu.Lock()
	runtime.running = true
	runtime.runCtx = context.Background()
	runtime.mu.Unlock()
	runtime.remember(first)
	key := proactiveReplyBatchKey(first)
	runtime.proactiveBatches[key] = &proactiveReplyBatch{
		items: []proactiveReplyCandidate{{
			Event:      first,
			Text:       first.RawMessage,
			QueuedAt:   time.Now(),
			Generation: 1,
		}},
		startedAt:  time.Now(),
		generation: 1,
	}

	runtime.flushProactiveReplyBatch(context.Background(), key, 1)

	channel.mu.Lock()
	sent := append([]OutgoingMessage(nil), channel.sent...)
	channel.mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want exactly one merged reply", sent)
	}
	if sent[0].ReplyMessageID != second.MessageID || sent[0].Text != "对，后一张是要乐奈，前一条和图片应当合在一起看" {
		t.Fatalf("merged reply = %#v, want reply to the later message", sent[0])
	}
	if provider.routeCalls != 2 || provider.replyCalls != 2 {
		t.Fatalf("route calls=%d reply calls=%d, want one bounded reroute", provider.routeCalls, provider.replyCalls)
	}
	if !strings.Contains(provider.lastRoutePayload, `"message_id":"message-1"`) || !strings.Contains(provider.lastRoutePayload, `"message_id":"message-2"`) {
		t.Fatalf("reroute did not receive both candidates: %s", provider.lastRoutePayload)
	}
	var superseded bool
	for _, entry := range logs.entries {
		if entry.Action == "diana.proactive_reply_superseded" && entry.Metadata["stage"] == "before_send" {
			superseded = true
		}
	}
	if !superseded {
		t.Fatalf("superseded audit log missing: %#v", logs.entries)
	}
	runtime.proactiveBatchMu.Lock()
	_, pending := runtime.proactiveBatches[key]
	runtime.proactiveBatchMu.Unlock()
	if pending {
		t.Fatal("merged proactive batch was not cleared")
	}
}

func TestProactiveReplyBatchCollectsPerSenderAndCanBeCancelled(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.mu.Lock()
	runtime.running = true
	runtime.runCtx = ctx
	runtime.mu.Unlock()

	first := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1"}
	second := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-2"}
	if !runtime.enqueueProactiveReply(first, "第一条") || !runtime.enqueueProactiveReply(second, "第二条") {
		t.Fatal("running runtime should enqueue proactive candidates")
	}
	runtime.proactiveBatchMu.Lock()
	batch := runtime.proactiveBatches[proactiveReplyBatchKey(first)]
	itemCount := 0
	if batch != nil {
		itemCount = len(batch.items)
	}
	runtime.proactiveBatchMu.Unlock()
	if itemCount != 2 {
		t.Fatalf("batch item count = %d, want 2", itemCount)
	}

	runtime.cancelProactiveReplyBatch(first)
	runtime.proactiveBatchMu.Lock()
	_, exists := runtime.proactiveBatches[proactiveReplyBatchKey(first)]
	runtime.proactiveBatchMu.Unlock()
	if exists {
		t.Fatal("explicit group trigger should cancel its pending proactive batch")
	}
}

func TestProactiveReplyBatchDoesNotMixSendersInSameGroup(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.mu.Lock()
	runtime.running = true
	runtime.runCtx = ctx
	runtime.mu.Unlock()

	first := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-a", MessageID: "message-a"}
	second := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-b", MessageID: "message-b"}
	if !runtime.enqueueProactiveReply(first, "A 的问题") || !runtime.enqueueProactiveReply(second, "B 的消息") {
		t.Fatal("running runtime should enqueue proactive candidates")
	}
	firstKey, secondKey := proactiveReplyBatchKey(first), proactiveReplyBatchKey(second)
	if firstKey == secondKey {
		t.Fatalf("different senders shared proactive key %q", firstKey)
	}
	runtime.proactiveBatchMu.Lock()
	firstBatch, secondBatch := runtime.proactiveBatches[firstKey], runtime.proactiveBatches[secondKey]
	runtime.proactiveBatchMu.Unlock()
	if firstBatch == nil || len(firstBatch.items) != 1 || secondBatch == nil || len(secondBatch.items) != 1 {
		t.Fatalf("sender batches first=%#v second=%#v", firstBatch, secondBatch)
	}

	runtime.cancelProactiveReplyBatch(second)
	runtime.proactiveBatchMu.Lock()
	_, firstExists := runtime.proactiveBatches[firstKey]
	_, secondExists := runtime.proactiveBatches[secondKey]
	runtime.proactiveBatchMu.Unlock()
	if !firstExists || secondExists {
		t.Fatalf("sender cancellation crossed batch boundary: first=%v second=%v", firstExists, secondExists)
	}
}

func TestProactiveReplyBatchAppliesRelationshipDeltaWithoutDoubleCounting(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","directed_at_bot":false,"answerable":true}`,
		`{"action":"none","prompt":""}`,
		"可以先检查错误日志里的第一条异常。",
		`{"should_update":true,"delta":1,"confidence":0.96,"reason":"初识阶段的一次真实提问会带来轻微熟悉"}`,
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		OwnerID:                 "owner",
		AgentEnabled:            false,
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	memory := newMemoryUserMemoryStore()
	memory.profiles["user-1"] = UserMemoryProfile{UserID: "user-1", MessageCount: 1}
	runtime.SetUserMemoryStore(memory)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "message-1",
		SenderName: "Alice",
		RawMessage: "这个报错应该怎么处理？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个报错应该怎么处理？"}}},
	}
	key := proactiveReplyBatchKey(event)
	runtime.proactiveBatches[key] = &proactiveReplyBatch{
		items:      []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}},
		generation: 1,
	}

	runtime.flushProactiveReplyBatch(context.Background(), key, 1)
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !runtime.waitForRelationshipEvaluations(waitCtx) {
		t.Fatal("relationship evaluation did not finish")
	}

	profile := memory.profiles[event.UserID]
	if profile.Favorability != 1 || profile.MessageCount != 1 {
		t.Fatalf("profile = %#v, want favorability 1 and existing message count 1", profile)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "可以先检查错误日志里的第一条异常" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if len(provider.requests) != 5 {
		t.Fatalf("provider calls = %d, want route, visual intent, reply, quality review, and relationship", len(provider.requests))
	}
	var relationshipLogFound bool
	for _, entry := range logs.entries {
		if entry.Action == "diana.relationship_evaluation" && entry.Metadata["delta"] == 1 {
			relationshipLogFound = true
		}
	}
	if !relationshipLogFound {
		t.Fatalf("relationship evaluation log missing: %#v", logs.entries)
	}
}

func TestProactiveReplyBatchDoesNotEvaluateUnselectedMessages(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":false,"confidence":0.99,"category":"no_response","target_message_id":"none"}`,
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		OwnerID:                 "owner",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	memory := newMemoryUserMemoryStore()
	memory.profiles["user-1"] = UserMemoryProfile{UserID: "user-1", MessageCount: 1}
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1"}
	key := proactiveReplyBatchKey(event)
	runtime.proactiveBatches[key] = &proactiveReplyBatch{
		items:      []proactiveReplyCandidate{{Event: event, Text: "我先去吃饭了"}},
		generation: 1,
	}

	runtime.flushProactiveReplyBatch(context.Background(), key, 1)

	profile := memory.profiles[event.UserID]
	if profile.Favorability != 0 || profile.MessageCount != 1 || len(channel.sent) != 0 || len(provider.requests) != 1 {
		t.Fatalf("profile=%#v sent=%#v provider calls=%d", profile, channel.sent, len(provider.requests))
	}
}

func TestProactiveReplyBatchDoesNotAwardFavorabilityWhenReplyFails(t *testing.T) {
	provider := &proactiveBatchReplyFailureProvider{}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		OwnerID:                 "owner",
		AgentEnabled:            false,
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	memory := newMemoryUserMemoryStore()
	memory.profiles["user-1"] = UserMemoryProfile{UserID: "user-1", MessageCount: 1}
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "message-1",
		SenderName: "Alice",
		RawMessage: "这个报错应该怎么处理？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个报错应该怎么处理？"}}},
	}
	key := proactiveReplyBatchKey(event)
	runtime.proactiveBatches[key] = &proactiveReplyBatch{
		items:      []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}},
		generation: 1,
	}

	runtime.flushProactiveReplyBatch(context.Background(), key, 1)

	profile := memory.profiles[event.UserID]
	if profile.Favorability != 0 || profile.MessageCount != 1 {
		t.Fatalf("profile = %#v, want unchanged relationship and message count", profile)
	}
	if provider.relationshipCalls != 0 {
		t.Fatalf("relationship evaluator calls = %d, want 0 after failed reply", provider.relationshipCalls)
	}
	if len(channel.sent) != 1 || !strings.HasPrefix(channel.sent[0].Text, "出错了：") {
		t.Fatalf("sent = %#v, want only the generic error reply", channel.sent)
	}
}

func TestProactiveReplyBatchRechecksSuppressionAfterRouting(t *testing.T) {
	provider := &proactiveRouteSuppressionProvider{}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		OwnerID:                 "owner",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "message-1",
		SenderName: "Alice",
		RawMessage: "这个报错应该怎么处理？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个报错应该怎么处理？"}}},
	}
	provider.runtime = runtime
	provider.event = event
	key := proactiveReplyBatchKey(event)
	runtime.proactiveBatches[key] = &proactiveReplyBatch{
		items:      []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}},
		generation: 1,
	}

	runtime.flushProactiveReplyBatch(context.Background(), key, 1)

	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want only proactive routing", provider.calls)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("suppressed proactive candidate still replied: %#v", channel.sent)
	}
	if _, active := runtime.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("route-time response suppression was not activated")
	}
}

type proactiveBatchReplyFailureProvider struct {
	relationshipCalls int
}

func (p *proactiveBatchReplyFailureProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	switch {
	case requestMessagesContain(req.Messages, "Intent Recognition"):
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","directed_at_bot":false,"answerable":true}`}, nil
	case requestMessagesContain(req.Messages, "功能路由器"):
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"none","prompt":""}`}, nil
	case requestMessagesContain(req.Messages, "关系变化评估器"):
		p.relationshipCalls++
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"should_update":true,"delta":1,"confidence":0.96,"reason":"初识阶段的一次真实互动"}`}, nil
	default:
		return nil, errors.New("reply failed")
	}
}

type proactiveRouteSuppressionProvider struct {
	runtime *Runtime
	event   MessageEvent
	calls   int
}

type proactiveReplyRerouteProvider struct {
	runtime          *Runtime
	second           MessageEvent
	injected         bool
	routeCalls       int
	replyCalls       int
	lastRoutePayload string
}

func (p *proactiveReplyRerouteProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	switch {
	case requestMessagesContain(req.Messages, "should_send"):
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"should_send":true,"confidence":0.99,"reason":"测试回复通过准确度审核"}`}, nil
	case requestMessagesContain(req.Messages, "Intent Recognition"):
		p.routeCalls++
		p.lastRoutePayload = req.Messages[len(req.Messages)-1].Content
		target := "message-1"
		if p.routeCalls > 1 {
			target = "message-2"
		}
		return &llm.GenerateResponse{
			Provider: llm.ProviderOpenAICompatible,
			Model:    "test",
			Text:     `{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"` + target + `","directed_at_bot":false,"answerable":true}`,
		}, nil
	case requestMessagesContain(req.Messages, "功能路由器"):
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`}, nil
	default:
		p.replyCalls++
		if !p.injected {
			p.injected = true
			p.runtime.remember(p.second)
			if !p.runtime.enqueueProactiveReply(p.second, p.second.RawMessage) {
				return nil, errors.New("could not enqueue continuation")
			}
			return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "这是第一条的旧回复。"}, nil
		}
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "对，后一张是要乐奈，前一条和图片应当合在一起看。"}, nil
	}
}

func (p *proactiveRouteSuppressionProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	if !requestMessagesContain(req.Messages, "Intent Recognition") {
		return nil, errors.New("suppressed proactive candidate reached reply generation")
	}
	p.runtime.activateReplySuppression(p.event, "test threshold reached", time.Now())
	return &llm.GenerateResponse{
		Provider: llm.ProviderOpenAICompatible,
		Model:    "test",
		Text:     `{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","directed_at_bot":false,"answerable":true}`,
	}, nil
}
