// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// napcatFriendRejection 是实测那次的原话（NapCat result=16）：对方把机器人删了
// 好友，之后每条私聊回复都被同一句挡回来。
const napcatFriendRejection = "send private message rejected: result=16 err=发送失败，请先添加对方为好友"

type rejectingQueueChannel struct {
	*queueTestChannel
	mu       sync.Mutex
	attempts int
	err      error
}

func (c *rejectingQueueChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	if c.err != nil {
		return c.err
	}
	return c.queueTestChannel.Send(context.Background(), msg)
}

func (c *rejectingQueueChannel) sendAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

func TestIsPermanentSendRejection(t *testing.T) {
	rejected := &outboundSendError{Cause: errors.New("diana: send failed after 1 attempts: " + napcatFriendRejection)}
	if !isPermanentSendRejection(rejected) {
		t.Fatal("result=16 friend rejection was not classified as permanent")
	}
	transient := &outboundSendError{Cause: errors.New("diana: send failed after 3 attempts: websocket: close 1006")}
	if isPermanentSendRejection(transient) {
		t.Fatal("a transport failure must stay retryable")
	}
	// 同一句话出现在别的环节不能让整条消息落终态。
	if isPermanentSendRejection(errors.New("工具输出里引用了一条报错：请先添加对方为好友")) {
		t.Fatal("a non-send error was misclassified as a permanent send rejection")
	}
	if isPermanentSendRejection(nil) || isPermanentSendRejection(context.DeadlineExceeded) {
		t.Fatal("nil or cancellation must never count as a permanent rejection")
	}
}

// TestPermanentSendRejectionDropsWithoutRegenerating 上游明说这条发不出去时
// 只跑一次：以前它会按 inboundMaxAttempts 重跑五轮，等于同一条消息重新生成
// 五遍回复再被拒五次。
func TestPermanentSendRejectionDropsWithoutRegenerating(t *testing.T) {
	store := newMemoryInboundEventStore()
	channel := &rejectingQueueChannel{queueTestChannel: newQueueTestChannel(), err: errors.New(napcatFriendRejection)}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`, "第一次回复",
		`{"action":"none","prompt":""}`, "不该有的第二次回复",
		`{"action":"none","prompt":""}`, "不该有的第三次回复",
	}}
	runtime := newQueuedTestRuntime(channel, store, provider)

	event := MessageEvent{
		Kind: EventKindPrivate, Time: time.Now().Unix(), SelfID: "42", UserID: "380726517",
		MessageID: "rejected-1", RawMessage: "在吗",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}},
	}
	id, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 5*time.Second, func() bool { return store.isDone(id) })
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}

	outcome, attempts := store.outcomeAndAttempts(id)
	if outcome != inboundOutcomeSendRejected {
		t.Fatalf("outcome = %q, want %s", outcome, inboundOutcomeSendRejected)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want exactly 1 — a permanent rejection must not be retried", attempts)
	}
	if got := channel.sendAttempts(); got != 1 {
		t.Fatalf("channel send attempts = %d, want 1", got)
	}
	// 原始错误留在事件明细里（record.Error → processing_error）。
	var audited bool
	store.mu.Lock()
	for _, entry := range store.audits {
		if entry.MessageID == event.MessageID && strings.Contains(entry.Error, "请先添加对方为好友") {
			audited = true
		}
	}
	store.mu.Unlock()
	if !audited {
		t.Fatal("original rejection was not preserved for the event detail view")
	}
}

// TestPermanentSendRejectionOutcomeIsExplained 事件页要说清楚为什么不再重试，
// 否则看起来像还在排队。
func TestPermanentSendRejectionOutcomeIsExplained(t *testing.T) {
	decision, reason, handled := DescribeEventOutcome(inboundOutcomeSendRejected)
	if decision != "error" || handled {
		t.Fatalf("decision=%q handled=%v", decision, handled)
	}
	if !strings.Contains(reason, "重试不可能成功") {
		t.Fatalf("reason = %q", reason)
	}
}
