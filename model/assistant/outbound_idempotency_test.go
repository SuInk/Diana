// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

func TestOutboundStepKeyCombinesPositionAndPayload(t *testing.T) {
	msg := OutgoingMessage{GroupID: "g1", Text: "同一句话"}
	first := &outboundTurn{id: "turn-1"}
	second := &outboundTurn{id: "turn-1"}
	// 同一轮里内容相同的两条消息占据不同序号，不会互相误判为重复。
	if a, b := first.nextStepKey(outgoingMessageFingerprint(msg)), first.nextStepKey(outgoingMessageFingerprint(msg)); a == b {
		t.Fatalf("two sends in one turn share the step key: %q", a)
	}
	// 重跑时序号从头开始，同一位置的同一载荷才算同一步。
	if a, b := second.nextStepKey(outgoingMessageFingerprint(msg)), (&outboundTurn{id: "turn-1"}).nextStepKey(outgoingMessageFingerprint(msg)); a != b {
		t.Fatalf("retry step key = %q, want %q", b, a)
	}
	changed := OutgoingMessage{GroupID: "g1", Text: "换了一句"}
	if a, b := (&outboundTurn{}).nextStepKey(outgoingMessageFingerprint(msg)), (&outboundTurn{}).nextStepKey(outgoingMessageFingerprint(changed)); a == b {
		t.Fatalf("different payloads share the step key: %q", a)
	}
	images := OutgoingMessage{GroupID: "g1", Text: "同一句话", ImageURLs: []string{"http://host/1.jpg"}}
	if a, b := (&outboundTurn{}).nextStepKey(outgoingMessageFingerprint(msg)), (&outboundTurn{}).nextStepKey(outgoingMessageFingerprint(images)); a == b {
		t.Fatalf("attached image did not change the step key: %q", a)
	}
}

func TestSendOutgoingSkipsStepsAlreadyDeliveredInAnEarlierAttempt(t *testing.T) {
	store := newMemoryInboundEventStore()
	channel := newQueueTestChannel()
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}
	msg := OutgoingMessage{GroupID: "123", Text: "抖音解析结果"}

	first := withOutboundTurn(context.Background(), "turn-1")
	if _, err := runtime.sendOutgoingWithResult(first, event, msg); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("sent count after first attempt = %d, want 1", got)
	}

	// 队列重跑：同一条入站事件、同一位置、同一载荷，不应该再发一次。
	retry := withOutboundTurn(context.Background(), "turn-1")
	if _, err := runtime.sendOutgoingWithResult(retry, event, msg); err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("sent count after retry = %d, want 1", got)
	}

	// 重跑时内容变了就必须真的发出去，不能被账本吞掉。
	changed := withOutboundTurn(context.Background(), "turn-1")
	if _, err := runtime.sendOutgoingWithResult(changed, event, OutgoingMessage{GroupID: "123", Text: "换了内容"}); err != nil {
		t.Fatalf("changed send: %v", err)
	}
	if got := channel.sentCount(); got != 2 {
		t.Fatalf("sent count after changed payload = %d, want 2", got)
	}

	// 换一条入站事件时账本互不影响。
	other := withOutboundTurn(context.Background(), "turn-2")
	if _, err := runtime.sendOutgoingWithResult(other, event, msg); err != nil {
		t.Fatalf("other turn send: %v", err)
	}
	if got := channel.sentCount(); got != 3 {
		t.Fatalf("sent count for a different turn = %d, want 3", got)
	}

	runtime.clearOutboundSteps("turn-1")
	replay := withOutboundTurn(context.Background(), "turn-1")
	if _, err := runtime.sendOutgoingWithResult(replay, event, msg); err != nil {
		t.Fatalf("send after cleanup: %v", err)
	}
	if got := channel.sentCount(); got != 4 {
		t.Fatalf("sent count after ledger cleanup = %d, want 4", got)
	}
}

func TestSendOutgoingStillSendsWithoutATurnOrLedger(t *testing.T) {
	channel := newQueueTestChannel()
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", MessageID: "m-1"}
	msg := OutgoingMessage{GroupID: "123", Text: "没有账本也要发出去"}
	for i := 0; i < 2; i++ {
		if _, err := runtime.sendOutgoingWithResult(context.Background(), event, msg); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if got := channel.sentCount(); got != 2 {
		t.Fatalf("sent count without a ledger = %d, want 2", got)
	}
}

func TestInboundRetriesExhaustedStopsAtTheAttemptCap(t *testing.T) {
	if inboundRetriesExhausted(inboundMaxAttempts - 1) {
		t.Fatal("stopped retrying before reaching the cap")
	}
	if !inboundRetriesExhausted(inboundMaxAttempts) {
		t.Fatal("kept retrying at the cap")
	}
	if decision, _, handled := DescribeEventOutcome(inboundOutcomeRetriesExhausted); decision != "error" || handled {
		t.Fatalf("exhausted outcome = %q handled=%v", decision, handled)
	}
}
