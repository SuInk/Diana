// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 挂进接话评分等待批的那条，worker 先以 merged_into_backlog_turn 收工；评分后真的回了，
// 事件页的 outcome 要跟着改成实际结果。worker 自己定下的其他 outcome 不被改写。
func TestRecordInboundEventAuditReplacesParkedOutcome(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Date(2026, 11, 9, 15, 35, 29, 0, time.UTC)
	insertLatencyEvent(t, s, "evt-parked", "30001", `{}`, at)
	insertLatencyEvent(t, s, "evt-replied", "30002", `{}`, at)
	if _, err := s.db.Exec(`UPDATE inbound_events SET outcome = 'merged_into_backlog_turn' WHERE id = 'evt-parked'`); err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"30001", "30002"} {
		record := assistant.EventRecord{
			At: at.Add(20 * time.Second), Kind: assistant.EventKindGroup, GroupID: "10001", UserID: "20002", MessageID: messageID,
			Outcome: "replied_proactive", Decision: "replied", Reply: "好",
		}
		if err := s.RecordInboundEventAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	// 只带决策、不带 outcome 的记录不清空占位。
	if err := s.RecordInboundEventAudit(ctx, assistant.EventRecord{Kind: assistant.EventKindGroup, GroupID: "10001", UserID: "20002", MessageID: "30001", Decision: "replied", Reply: "好"}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"evt-parked": "replied_proactive", "evt-replied": "replied"} {
		var got string
		if err := s.db.QueryRow(`SELECT outcome FROM inbound_events WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s outcome=%q, want %q", id, got, want)
		}
	}
}

// 同一条记录会写两次（同步一次、事件监听异步一次），等待批的占位那次可能晚于真实结论落库，
// 真实结论也可能赶在 worker 收工前到。不管先后，事件页都得停在真实结论上。
func TestRecordInboundEventAuditParkedPlaceholderNeverOverwritesVerdict(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 0, 37, 44, 0, time.UTC)
	insertLatencyEvent(t, s, "evt-parked", "30001", `{}`, at)
	if _, err := s.db.Exec(`UPDATE inbound_events SET status = 'processing', outcome = NULL, lease_owner = 'worker-1' WHERE id = 'evt-parked'`); err != nil {
		t.Fatal(err)
	}
	record := func(outcome, reason string) {
		t.Helper()
		if err := s.RecordInboundEventAudit(ctx, assistant.EventRecord{
			Kind: assistant.EventKindGroup, GroupID: "10001", UserID: "20002", MessageID: "30001",
			Outcome: outcome, Decision: "not_replied", Reason: reason,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(parkedProactiveRouteOutcome, "排队等接话判断")
	// 定时器那一轮的结论先于 worker 收工落库。
	record("ignored", "在跟机器人说话：否")
	// 占位那次的异步写晚到。
	record(parkedProactiveRouteOutcome, "排队等接话判断")
	if err := s.CompleteInboundEvent(ctx, "evt-parked", "worker-1", parkedProactiveRouteOutcome); err != nil {
		t.Fatal(err)
	}
	var outcome, reason string
	if err := s.db.QueryRow(`SELECT outcome, decision_reason FROM inbound_events WHERE id = 'evt-parked'`).Scan(&outcome, &reason); err != nil {
		t.Fatal(err)
	}
	if outcome != "ignored" || reason != "在跟机器人说话：否" {
		t.Fatalf("outcome=%q reason=%q, want ignored + 真实结论", outcome, reason)
	}
}
