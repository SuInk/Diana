package storage

import (
	"context"
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
