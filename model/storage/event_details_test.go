// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	if page.Total != 3 || page.FilteredTotal != 3 || page.Replied != 1 || page.NotReplied != 1 || page.Pending != 1 || page.Errors != 0 {
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

	for _, test := range []struct {
		name   string
		filter InboundEventResultFilter
		wantID string
	}{
		{name: "replied", filter: InboundEventResultReplied, wantID: "replied"},
		{name: "not replied", filter: InboundEventResultNotReplied, wantID: "ignored"},
		{name: "pending", filter: InboundEventResultPending, wantID: "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filtered, err := store.ListInboundEventDetails(ctx, now.Add(-24*time.Hour), 1, 0, test.filter)
			if err != nil {
				t.Fatal(err)
			}
			if filtered.Total != 3 || filtered.FilteredTotal != 1 || len(filtered.Events) != 1 || filtered.Events[0].ID != test.wantID {
				t.Fatalf("filtered page = %+v, events = %#v", filtered, filtered.Events)
			}
		})
	}

	if _, err := store.db.ExecContext(ctx, `
UPDATE inbound_events
SET decision = 'error', processing_error = 'vision request failed'
WHERE id = 'ignored'
`); err != nil {
		t.Fatal(err)
	}
	errorPage, err := store.ListInboundEventDetails(ctx, now.Add(-24*time.Hour), 1, 0, InboundEventResultError)
	if err != nil {
		t.Fatal(err)
	}
	if errorPage.Total != 3 || errorPage.Errors != 1 || errorPage.FilteredTotal != 1 || len(errorPage.Events) != 1 || errorPage.Events[0].ID != "ignored" {
		t.Fatalf("error page = %+v, events = %#v", errorPage, errorPage.Events)
	}
	if errorPage.NotReplied != 0 || errorPage.Replied+errorPage.NotReplied+errorPage.Pending+errorPage.Errors != errorPage.Total {
		t.Fatalf("result categories are not exclusive: %+v", errorPage)
	}
	if _, err := store.ListInboundEventDetails(ctx, now.Add(-24*time.Hour), 1, 0, InboundEventResultFilter("unknown")); err == nil {
		t.Fatal("unsupported event result filter was accepted")
	}
}

func TestListInboundEventDetailsRendersStructuredImagesInsteadOfCQText(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-images.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{
		Platform:  assistant.PlatformOneBotV11,
		Kind:      assistant.EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-1",
		MessageID: "image-message",
		Time:      time.Now().Unix(),
		RawMessage: "说明 [CQ:image,summary=&#91;第一张&#93;,file=first.gif,url=https://example.com/first.gif]" +
			"[CQ:image,summary=&#91;第二张&#93;,file=second.png,url=https://example.com/second.png]",
		Segments: []assistant.MessageSegment{
			{Type: "text", Data: map[string]string{"text": "说明 "}},
			{Type: "image", Data: map[string]string{"summary": "[第一张]", "cached_file": "/cache/private-first.gif", "url": "https://example.com/first.gif"}},
			{Type: "image", Data: map[string]string{"summary": "[第二张]", "image_unavailable": "true", "url": "https://example.com/second.png"}},
			{Type: "image", Data: map[string]string{"source_type": "video_frame", "cached_file": "/cache/video-frame.jpg"}},
		},
	}
	eventID, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}

	page, err := store.ListInboundEventDetails(ctx, time.Now().Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %#v", page.Events)
	}
	got := page.Events[0]
	if got.Text != "说明" || strings.Contains(got.Text, "CQ:image") {
		t.Fatalf("display text = %q", got.Text)
	}
	if got.Platform != assistant.PlatformOneBotV11 || len(got.Images) != 2 {
		t.Fatalf("event detail = %#v", got)
	}
	if got.Images[0].Index != 1 || got.Images[0].Summary != "[第一张]" || got.Images[0].Unavailable {
		t.Fatalf("first image = %#v", got.Images[0])
	}
	if got.Images[1].Index != 2 || got.Images[1].Summary != "[第二张]" || !got.Images[1].Unavailable {
		t.Fatalf("second image = %#v", got.Images[1])
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/cache/private-first.gif") || strings.Contains(string(encoded), "example.com/first.gif") {
		t.Fatalf("event detail leaked image source: %s", encoded)
	}

	first, found, err := store.InboundEventImageSegment(ctx, eventID, 1)
	if err != nil || !found || first.Data["cached_file"] != "/cache/private-first.gif" {
		t.Fatalf("first segment found=%v err=%v segment=%#v", found, err, first)
	}
	second, found, err := store.InboundEventImageSegment(ctx, eventID, 2)
	if err != nil || !found || second.Data["image_unavailable"] != "true" {
		t.Fatalf("second segment found=%v err=%v segment=%#v", found, err, second)
	}
	if _, found, err := store.InboundEventImageSegment(ctx, eventID, 3); err != nil || found {
		t.Fatalf("third segment found=%v err=%v", found, err)
	}
}

func TestListInboundEventDetailsIncludesRecallNoticeAndOperator(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().Truncate(time.Second)
	recall := assistant.MessageEvent{
		Platform:     assistant.PlatformOneBotV11,
		Kind:         assistant.EventKindNotice,
		SubType:      "group_recall",
		Time:         now.Unix(),
		OriginalTime: now.Add(-time.Minute).Unix(),
		GroupID:      "group-1",
		UserID:       "user-1",
		SenderName:   "Alice",
		OperatorID:   "admin-1",
		OperatorName: "Carol",
		OperatorRole: "admin",
		MessageID:    "recalled-message",
		RawMessage:   "被撤回的正文",
	}
	if err := store.AppendMessageEvent(ctx, "group:group-1", recall); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordNoticeEvent(ctx, "group:group-1", recall); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListInboundEventDetails(ctx, now.Add(-time.Hour), 10, 0, InboundEventResultNotice)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Notices != 1 || page.NotReplied != 0 || page.FilteredTotal != 1 || len(page.Events) != 1 {
		t.Fatalf("recall page = %+v", page)
	}
	got := page.Events[0]
	if got.Kind != "notice" || got.SubType != "group_recall" || got.Text != "被撤回的正文" || got.SenderName != "Alice" ||
		got.OperatorID != "admin-1" || got.OperatorName != "Carol" || got.OperatorRole != "admin" || got.OriginalTime == nil {
		t.Fatalf("recall event = %#v", got)
	}
}

func TestRestoredSchemaBackfillsPersistedRecallNotices(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "event-recall-backfill.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	recall := assistant.MessageEvent{
		Kind: assistant.EventKindNotice, SubType: "group_recall", Time: now.Unix(),
		GroupID: "group-1", UserID: "user-1", OperatorID: "user-1", MessageID: "old-recall",
		RawMessage: "升级前保存的撤回消息",
	}
	if err := store.AppendMessageEvent(ctx, "group:group-1", recall); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	page, err := store.ListInboundEventDetails(ctx, now.Add(-time.Hour), 10, 0, InboundEventResultNotice)
	if err != nil {
		t.Fatal(err)
	}
	if page.Notices != 1 || len(page.Events) != 1 || page.Events[0].Text != "升级前保存的撤回消息" {
		t.Fatalf("backfilled recalls = %+v", page)
	}
}

func TestListInboundEventDetailsParsesLegacyCQOnlyImage(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "legacy-event-image.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	raw := "[CQ:image,summary=&#91;上吊&#93;,file=sticker.gif,url=https://example.com/sticker.gif]"
	event := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "group-1", UserID: "user-1",
		MessageID: "legacy-image", Time: time.Now().Unix(), RawMessage: raw,
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}

	page, err := store.ListInboundEventDetails(ctx, time.Now().Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Text != "" || len(page.Events[0].Images) != 1 || page.Events[0].Images[0].Summary != "[上吊]" {
		t.Fatalf("legacy image event = %#v", page.Events)
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

func TestInboundDeliveryAuditTracksAckAndSelfEcho(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "delivery-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	event := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "group-1", UserID: "user-1",
		MessageID: "source-message", Time: time.Now().Unix(),
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	for _, stage := range []assistant.OutboundDeliveryStage{
		assistant.OutboundDeliveryGenerated,
		assistant.OutboundDeliverySendAttempted,
		assistant.OutboundDeliveryAcknowledged,
	} {
		messageID := ""
		if stage == assistant.OutboundDeliveryAcknowledged {
			messageID = "outbound-42"
		}
		if err := store.RecordInboundEventDelivery(ctx, event, stage, messageID, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordInboundEventDelivery(ctx, event, assistant.OutboundDeliveryAcknowledged, "outbound-43", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventSelfEcho(ctx, "outbound-43", time.Now()); err != nil {
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
	if got.DeliveryStage != string(assistant.OutboundDeliveryEchoPersisted) || got.OutboundMessageID != "outbound-42,outbound-43" ||
		got.ReplyGeneratedAt == nil || got.SendAttemptedAt == nil || got.SendAckedAt == nil || got.SelfEchoAt == nil || got.DeliveryError != "" {
		t.Fatalf("delivery detail = %#v", got)
	}
}

func TestErrorRepliedRequiresDeliveryEvidenceForRepliedCount(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "error-delivery-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()
	for index, stage := range []string{"", string(assistant.OutboundDeliveryAcknowledged)} {
		id := fmt.Sprintf("error-%d", index)
		_, err := store.db.Exec(`
INSERT INTO inbound_events (
  id, session, kind, group_id, user_id, message_id, event_time, payload, priority,
  status, attempts, available_at, outcome, delivery_stage, created_at, updated_at, completed_at
) VALUES (?, 'group:g', 'group', 'g', 'u', ?, ?, '{}', 0, 'done', 1, ?, 'error_replied', ?, ?, ?, ?)
`, id, id, now.Unix(), now.UnixNano(), stage, now.UnixNano(), now.UnixNano(), now.UnixNano())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE inbound_events SET decision = 'replied' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListInboundEventDetails(ctx, now.Add(-time.Hour), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Replied != 1 || page.NotReplied != 0 || page.Errors != 1 {
		t.Fatalf("counts = replied=%d not=%d errors=%d", page.Replied, page.NotReplied, page.Errors)
	}
	errorPage, err := store.ListInboundEventDetails(ctx, now.Add(-time.Hour), 10, 0, InboundEventResultError)
	if err != nil {
		t.Fatal(err)
	}
	if len(errorPage.Events) != 1 || errorPage.Events[0].ID != "error-0" {
		t.Fatalf("error events = %#v", errorPage.Events)
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
	for _, name := range []string{
		"decision", "decision_reason", "reply_text", "processing_error", "duration_ms",
		"delivery_stage", "outbound_message_id", "reply_generated_at", "send_attempted_at", "send_acked_at", "self_echo_at", "delivery_error",
	} {
		if !found[name] {
			t.Fatalf("audit column %q was not migrated", name)
		}
	}
}
