package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

func TestListInboundEventDetailsFiltersPaginatesAndCountsDecisions(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-details.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().Truncate(time.Second)
	insert := func(id string, at time.Time, status, outcome, text string, duration time.Duration) {
		t.Helper()
		createdAt := at.UnixNano()
		completedAt := int64(0)
		if status == inboundStatusDone {
			completedAt = createdAt + duration.Nanoseconds()
		}
		_, err := store.db.ExecContext(ctx, `
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, 'group', 'g1', 'u1', ?, '测试成员', ?, ?, '{}', ?)
`, id, "group:g1", id, at.Unix(), text, at.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.db.ExecContext(ctx, `
INSERT INTO inbound_events (
  id, session, kind, group_id, user_id, message_id, event_time, payload, priority,
  status, attempts, available_at, outcome, created_at, updated_at, completed_at
)
VALUES (?, ?, 'group', 'g1', 'u1', ?, ?, '{}', 0, ?, 0, ?, ?, ?, ?, ?)
`, id, "group:g1", id, at.Unix(), status, createdAt, outcome, createdAt, createdAt, completedAt)
		if err != nil {
			t.Fatal(err)
		}
	}

	insert("replied", now.Add(-30*time.Minute), inboundStatusDone, "replied", "会回复", 1500*time.Millisecond)
	insert("ignored", now.Add(-2*time.Hour), inboundStatusDone, "ignored_member_level", "不回复", 0)
	insert("pending", now.Add(-3*time.Hour), inboundStatusPending, "", "等待中", 0)
	insert("old", now.Add(-40*24*time.Hour), inboundStatusDone, "ignored_stale", "很久以前", 0)
	for _, entry := range []applog.Entry{
		{
			Action:    "qqbot.llm_usage",
			Target:    "replied",
			CreatedAt: now.Add(-20 * time.Minute),
			Metadata:  map[string]any{"input_tokens": 100, "output_tokens": 40, "total_tokens": 0},
		},
		{
			Action:    "assistant.llm_usage",
			Target:    "ignored",
			CreatedAt: now.Add(-90 * time.Minute),
			Metadata:  map[string]any{"input_tokens": 20, "output_tokens": 10, "total_tokens": 35},
		},
		{
			Action:    "qqbot.llm_usage",
			Target:    "old",
			CreatedAt: now.Add(-40 * 24 * time.Hour),
			Metadata:  map[string]any{"input_tokens": 1000, "output_tokens": 1000},
		},
	} {
		if err := store.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	page, err := store.ListInboundEventDetails(ctx, now.Add(-24*time.Hour), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Replied != 1 || page.NotReplied != 1 || page.Pending != 1 || page.Errors != 0 {
		t.Fatalf("counts = %+v, want total/replied/not/pending/errors 3/1/1/1/0", page)
	}
	if len(page.Events) != 2 || page.Events[0].ID != "replied" || page.Events[1].ID != "ignored" {
		t.Fatalf("first page = %#v", page.Events)
	}
	if page.Events[0].SenderName != "测试成员" || page.Events[0].DurationMS != 1500 {
		t.Fatalf("replied detail = %#v", page.Events[0])
	}
	if page.LLMCalls != 2 || page.InputTokens != 120 || page.OutputTokens != 50 || page.TotalTokens != 175 {
		t.Fatalf("token totals = calls:%d input:%d output:%d total:%d, want 2/120/50/175", page.LLMCalls, page.InputTokens, page.OutputTokens, page.TotalTokens)
	}
	if page.Events[0].LLMCalls != 1 || page.Events[0].TotalTokens != 140 {
		t.Fatalf("replied token usage = %#v", page.Events[0])
	}
	if page.Events[1].LLMCalls != 1 || page.Events[1].TotalTokens != 35 {
		t.Fatalf("ignored token usage = %#v", page.Events[1])
	}

	next, err := store.ListInboundEventDetails(ctx, now.Add(-24*time.Hour), 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Events) != 1 || next.Events[0].ID != "pending" {
		t.Fatalf("second page = %#v", next.Events)
	}
}

func TestDescribeEventOutcomeExplainsReplyAndSilence(t *testing.T) {
	cases := map[string]string{
		"replied_direct_followup":      "直接回复了机器人",
		"ignored_member_level":         "最低回复等级",
		"ignored_response_suppression": "临时响应限制期",
		"ignored_ai_reply_loop":        "自动回复",
		"ignored_stale":                "离线恢复窗口",
		"dropped_outbound_delivery":    "投递失败",
	}
	for outcome, expected := range cases {
		_, reason, _ := assistant.DescribeEventOutcome(outcome)
		if !strings.Contains(reason, expected) {
			t.Fatalf("outcome %q reason = %q, want %q", outcome, reason, expected)
		}
	}
}

func TestRecordInboundEventAuditPersistsDecisionReasonAndReply(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{
		Kind:       assistant.EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "message-1",
		RawMessage: "这个问题怎么解决",
		Time:       time.Now().Unix(),
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "audit-test", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, item.ID, "audit-test", "replied_proactive"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{
		Kind:      event.Kind,
		GroupID:   event.GroupID,
		UserID:    event.UserID,
		MessageID: event.MessageID,
		Decision:  "replied",
		Reason:    "主动回复判断允许回复：问题明确且可回答",
		Reply:     "可以先检查错误日志。",
		Duration:  1340,
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListInboundEventDetails(ctx, time.Now().Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %#v", page.Events)
	}
	got := page.Events[0]
	if got.Decision != "replied" || got.Reason != "主动回复判断允许回复：问题明确且可回答" || got.Reply != "可以先检查错误日志。" || got.DurationMS != 1340 {
		t.Fatalf("audit detail = %#v", got)
	}
}

func TestInboundEventAuditColumnsMigrateExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-event-audit.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE inbound_events (
  id TEXT PRIMARY KEY,
  session TEXT NOT NULL,
  kind TEXT NOT NULL,
  group_id TEXT,
  user_id TEXT,
  message_id TEXT,
  event_time INTEGER NOT NULL,
  payload TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  available_at INTEGER NOT NULL,
  lease_owner TEXT,
  lease_until INTEGER,
  outcome TEXT,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER
)
`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rows, err := store.db.Query(`PRAGMA table_info(inbound_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, name := range []string{"decision", "decision_reason", "reply_text", "processing_error", "duration_ms"} {
		if !found[name] {
			t.Fatalf("audit column %q was not migrated", name)
		}
	}
}
