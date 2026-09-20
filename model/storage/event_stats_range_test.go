// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// insertRangeEvent 直接写一行已完成的队列事件，绕开入队/出队流程：这里要验的是统计
// 口径，不是队列状态机。
func insertRangeEvent(t *testing.T, s *SQLiteStore, id string, eventAt, completedAt time.Time, outcome, processingError string, durationMS int64) {
	t.Helper()
	var completed any
	if !completedAt.IsZero() {
		completed = completedAt.UnixNano()
	}
	_, err := s.db.Exec(`
INSERT INTO inbound_events (id, session, kind, event_time, payload, available_at, status, outcome, processing_error, duration_ms, created_at, updated_at, completed_at)
VALUES (?, 'session', 'group', ?, '{}', 0, 'done', ?, ?, ?, ?, ?, ?)
`, id, eventAt.Unix(), outcome, processingError, durationMS, eventAt.UnixNano(), eventAt.UnixNano(), completed)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEventStatsRangeCountsByDefinition(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	until := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	inside := until.Add(-30 * time.Minute)
	outside := until.Add(-2 * time.Hour)

	// 窗口内：一条普通回复、一条错误说明回复、一条发送失败、一条被忽略。
	insertRangeEvent(t, s, "replied", inside, inside, "replied", "", 1200)
	insertRangeEvent(t, s, "policy", inside, inside, "error_replied_content_policy", "", 800)
	insertRangeEvent(t, s, "dropped", inside, inside, "dropped_outbound_delivery", "发送连接不可用", 0)
	insertRangeEvent(t, s, "ignored", inside, inside, "ignored_policy", "", 0)
	// 窗口外的不该被算进来。
	insertRangeEvent(t, s, "old", outside, outside, "replied", "", 5000)

	stats, err := s.EventStatsRange(ctx, until.Add(-time.Hour), until)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Messages != 4 {
		t.Fatalf("Messages = %d, want 4", stats.Messages)
	}
	// error_replied_content_policy 也是「回复过」：只全等匹配 error_replied 会漏掉它。
	if stats.Handled != 2 {
		t.Fatalf("Handled = %d, want 2", stats.Handled)
	}
	// 错误看 processing_error，不看 outcome —— dropped_outbound_delivery 不属于
	// error_replied，但运行时确实把它记成了错误。
	if stats.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", stats.Errors)
	}
	// 平均只算回复了且有耗时的那两条，被忽略和没耗时的不该把均值拉低。
	if stats.RepliesMeasured != 2 || stats.AvgReplyMS != 1000 {
		t.Fatalf("AvgReplyMS = %d over %d samples, want 1000 over 2", stats.AvgReplyMS, stats.RepliesMeasured)
	}
}

func TestEventStatsRangeEmptyWindow(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	stats, err := s.EventStatsRange(context.Background(), now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	// 没有样本时平均值留 0，由 RepliesMeasured 说明「不是 0 毫秒，是没有数据」。
	if stats.Messages != 0 || stats.AvgReplyMS != 0 || stats.RepliesMeasured != 0 {
		t.Fatalf("empty window = %+v", stats)
	}

	if _, err := s.EventStatsRange(context.Background(), now, now); err == nil {
		t.Fatal("expected an error for an empty interval")
	}
}
