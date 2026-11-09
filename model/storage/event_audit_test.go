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
