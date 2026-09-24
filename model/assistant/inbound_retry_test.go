package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
)

type retryCaptureStore struct {
	*memoryInboundEventStore
	saved   []MessageEvent
	saveErr error
}

func (s *retryCaptureStore) SaveInboundRetry(_ context.Context, _ string, event MessageEvent, _ int) error {
	s.saved = append(s.saved, event)
	return s.saveErr
}
func (*retryCaptureStore) ReplayInboundRetries(context.Context, int) (int, error) { return 0, nil }

func TestRetainFailedInboundRequiresDurability(t *testing.T) {
	r := newQueuedTestRuntime(newQueueTestChannel(), newMemoryInboundEventStore(), nil)
	s := &retryCaptureStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	r.SetInboundEventStore(s)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", Platform: PlatformTelegram, Time: time.Now().Unix()}
	if err := r.retainFailedInbound(event, errors.New("database busy")); err != nil || len(s.saved) != 1 {
		t.Fatalf("not retained: %v", err)
	}
	if !r.takeFailedInbound().IsZero() {
		t.Fatal("telegram triggered onebot backfill")
	}
	s.saveErr = errors.New("disk full")
	if err := r.retainFailedInbound(event, errors.New("database busy")); err == nil {
		t.Fatal("journal failure was acknowledged")
	}
	r.setInboundReplayCutoff(time.Now().Add(-30 * time.Minute))
	event.Time = time.Now().Add(-2 * time.Hour).Unix()
	event.RetryRecovered = true
	if r.inboundEventIsStale(event, time.Now()) {
		t.Fatal("durable retry inside 24 hours discarded")
	}
	event.Time = time.Now().Add(-25 * time.Hour).Unix()
	if !r.inboundEventIsStale(event, time.Now()) {
		t.Fatal("expired retry accepted")
	}
}

// 没进队列的消息在事件明细里查不到，暂存和丢失都得在运行日志里留下是哪一条。
func TestRetainFailedInboundWritesAppLog(t *testing.T) {
	r := newQueuedTestRuntime(newQueueTestChannel(), newMemoryInboundEventStore(), nil)
	s := &retryCaptureStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	r.SetInboundEventStore(s)
	logs := &captureAppLogs{}
	r.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m-1", Platform: PlatformTelegram, Time: time.Now().Unix()}
	_ = r.retainFailedInbound(event, errors.New("database busy"))
	s.saveErr = errors.New("disk full")
	_ = r.retainFailedInbound(event, errors.New("database busy"))

	entries := logs.entriesSnapshot()
	if len(entries) != 2 || entries[0].Action != "inbound_event_retained" || entries[1].Action != "inbound_event_lost" {
		t.Fatalf("entries = %v", appLogActions(entries))
	}
	lost := entries[1]
	if lost.Level != "error" || lost.Target != "m-1" || lost.Metadata["group_id"] != "g" || lost.Detail == "" {
		t.Fatalf("lost entry = %#v", lost)
	}
}

func TestInboundFailureBackfillsWithoutReconnect(t *testing.T) {
	s := newMemoryInboundEventStore()
	now := time.Now().Unix()
	s.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: now}}
	channel := newQueueTestChannel()
	logs := &captureAppLogs{}
	r := newQueuedTestRuntime(channel, s, nil)
	r.SetAppLogWriter(logs)
	startTestRuntime(t, r)
	waitForCondition(t, 4*time.Second, func() bool { return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed") })
	channel.setResponse("get_group_msg_history", map[string]any{"messages": []any{historyTestMessage(967, now-1800, "recovered")}})
	r.noteFailedInbound(MessageEvent{Platform: PlatformOneBotV11, Time: now - 1800})
	waitForCondition(t, 4*time.Second, func() bool { return s.hasEvent("group:123:967") })
	if !hasAppLogAction(logs.entriesSnapshot(), "backfill_ingest_recovery") {
		t.Fatal("missing automatic recovery audit")
	}
}
