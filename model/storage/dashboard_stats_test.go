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

func TestDashboardEventStatsSnapshotRestoresOnlyCompletedDeduplicatedMessages(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dashboard-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, time.July, 19, 15, 30, 0, 0, time.Local)
	insertInbound := func(id string, kind assistant.EventKind, at time.Time, status string, outcome string, duration time.Duration) {
		t.Helper()
		completedAt := int64(0)
		createdAt := at.UnixNano()
		if status == inboundStatusDone {
			completedAt = createdAt + duration.Nanoseconds()
		}
		_, err := store.db.ExecContext(ctx, `
INSERT INTO inbound_events (
  id, session, kind, event_time, payload, priority, status, attempts,
  available_at, outcome, created_at, updated_at, completed_at
)
VALUES (?, ?, ?, ?, '{}', 0, ?, 0, ?, ?, ?, ?, ?)
`, id, string(kind)+":"+id, string(kind), at.Unix(), status, createdAt, outcome, createdAt, createdAt, completedAt)
		if err != nil {
			t.Fatal(err)
		}
	}

	insertInbound("replied", assistant.EventKindGroup, now.Add(-30*time.Minute), inboundStatusDone, "replied", time.Second)
	insertInbound("error", assistant.EventKindPrivate, now.Add(-90*time.Minute), inboundStatusDone, "error_replied", 3*time.Second)
	insertInbound("ignored", assistant.EventKindGroup, now.Add(-2*time.Hour), inboundStatusDone, "ignored", 0)
	insertInbound("old", assistant.EventKindPrivate, now.Add(-72*time.Hour), inboundStatusDone, "replied_proactive_batch", 5*time.Second)
	insertInbound("pending", assistant.EventKindGroup, now.Add(-15*time.Minute), inboundStatusPending, "", 0)
	insertInbound("stale", assistant.EventKindGroup, now.Add(-10*time.Minute), inboundStatusDone, "ignored_stale", 0)

	legacy := assistant.MessageEvent{
		Kind:       assistant.EventKindGroup,
		Time:       now.Add(-3 * time.Hour).Unix(),
		GroupID:    "legacy-group",
		UserID:     "legacy-user",
		MessageID:  "legacy-message",
		RawMessage: "legacy inbound",
	}
	if err := store.AppendMessageEvent(ctx, "group:legacy", legacy); err != nil {
		t.Fatal(err)
	}
	// Old assistant-history rows have no message ID. They must not be counted as
	// another inbound message when restoring the Dashboard baseline.
	legacy.MessageID = ""
	legacy.RawMessage = "legacy bot reply"
	if err := store.AppendMessageEvent(ctx, "group:legacy", legacy); err != nil {
		t.Fatal(err)
	}

	stats, err := store.DashboardEventStatsSnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalEvents != 5 || stats.HandledEvents != 3 || stats.ErrorEvents != 1 {
		t.Fatalf("totals = total:%d handled:%d errors:%d, want 5/3/1", stats.TotalEvents, stats.HandledEvents, stats.ErrorEvents)
	}
	if stats.ByKind[string(assistant.EventKindGroup)] != 3 || stats.ByKind[string(assistant.EventKindPrivate)] != 2 {
		t.Fatalf("by kind = %#v, want group:3 private:2", stats.ByKind)
	}
	if stats.DurationCount != 3 || stats.DurationTotalMS != 9000 {
		t.Fatalf("duration = %dms/%d, want 9000ms/3", stats.DurationTotalMS, stats.DurationCount)
	}
	if stats.LastEventAt.Unix() != now.Add(-30*time.Minute).Unix() {
		t.Fatalf("last event = %s, want %s", stats.LastEventAt, now.Add(-30*time.Minute))
	}
	var recentTotal, recentHandled, recentErrors int64
	for _, bucket := range stats.Hourly {
		recentTotal += bucket.Total
		recentHandled += bucket.Handled
		recentErrors += bucket.Errors
	}
	if recentTotal != 4 || recentHandled != 2 || recentErrors != 1 {
		t.Fatalf("recent buckets = total:%d handled:%d errors:%d, want 4/2/1", recentTotal, recentHandled, recentErrors)
	}
}

// 重启后恢复的历史基线也要按机器人分开，否则切过去那台会顶着别人的累计量。
func TestDashboardEventStatsSnapshotSplitsByProfile(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dashboard-baseline-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, time.July, 19, 15, 30, 0, 0, time.Local)
	insert := func(id string, profileID string, outcome string, at time.Time) {
		t.Helper()
		createdAt := at.UnixNano()
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO inbound_events (
  id, session, kind, event_time, payload, priority, status, attempts,
  available_at, outcome, created_at, updated_at, completed_at, profile_id
)
VALUES (?, ?, 'group', ?, '{}', 0, 'done', 0, ?, ?, ?, ?, ?, ?)
`, id, "group:"+id, at.Unix(), createdAt, outcome, createdAt, createdAt, createdAt+int64(time.Second), profileID); err != nil {
			t.Fatal(err)
		}
	}
	insert("qq-1", "qq", "replied", now.Add(-time.Hour))
	insert("qq-2", "qq", "error_replied", now.Add(-30*time.Minute))
	insert("tg-1", "tg", "replied", now.Add(-15*time.Minute))

	baselines, err := store.DashboardEventStatsSnapshotByProfile(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := baselines[""]; got.TotalEvents != 3 || got.HandledEvents != 3 || got.ErrorEvents != 1 {
		t.Fatalf("aggregate = total:%d handled:%d errors:%d, want 3/3/1", got.TotalEvents, got.HandledEvents, got.ErrorEvents)
	}
	if got := baselines["qq"]; got.TotalEvents != 2 || got.ErrorEvents != 1 {
		t.Fatalf("qq baseline = total:%d errors:%d, want 2/1", got.TotalEvents, got.ErrorEvents)
	}
	if got := baselines["tg"]; got.TotalEvents != 1 || got.ErrorEvents != 0 {
		t.Fatalf("tg baseline = total:%d errors:%d, want 1/0", got.TotalEvents, got.ErrorEvents)
	}
	// 旧接口继续返回合计，调用方不用一起改。
	total, err := store.DashboardEventStatsSnapshot(ctx, now)
	if err != nil || total.TotalEvents != 3 {
		t.Fatalf("aggregate snapshot = %d err=%v", total.TotalEvents, err)
	}
}

func TestDashboardBaselineCountsDeliveryFailuresAfterRestart(t *testing.T) {
	store := openOutboundDeliveryTestStore(t)
	now := time.Now()
	for _, outcome := range []string{"dropped_outbound_delivery", "dropped_send_rejected", "dropped_retries_exhausted"} {
		if _, err := store.db.Exec(`INSERT INTO inbound_events(id, session, kind, profile_id, event_time, payload, status, outcome, available_at, created_at, updated_at, completed_at)
VALUES (?, 'group:g', 'group', 'profile-a', ?, '{}', 'done', ?, 0, ?, ?, ?)`, outcome, now.Unix(), outcome, now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.DashboardEventStatsSnapshotByProfile(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	rangeStats, err := store.EventStatsRange(context.Background(), now.Add(-time.Hour), now.Add(time.Second))
	if err != nil || rangeStats.Errors != 3 {
		t.Fatalf("range errors = %d, err=%v", rangeStats.Errors, err)
	}
	if stats[""].ErrorEvents != 3 || stats["profile-a"].ErrorEvents != 3 {
		t.Fatalf("lost delivery errors: %+v", stats)
	}
	page, err := store.ListInboundEventDetails(context.Background(), InboundEventQuery{Since: now.Add(-time.Hour), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Errors != 3 || page.NotReplied != 0 {
		t.Fatalf("delivery failures counted as silence: errors=%d silent=%d", page.Errors, page.NotReplied)
	}
}
