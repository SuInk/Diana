// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

func TestDebugTraceIsStoredBesideDatabaseNotInAppLogs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m:1/2", Time: time.Now().Unix()}
	eventID, _, err := store.EnqueueInboundEvent(ctx, "group:g", event)
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"kind": "group", "group_id": "g", "user_id": "u", "request": strings.Repeat("上下文", 1000)}
	base := time.Now().UTC()
	for index, message := range []string{"first", "second"} {
		if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "debug_trace", Target: event.MessageID, Message: message, Metadata: meta, CreatedAt: base.Add(time.Duration(index) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	// 跨群检索记录仍在 app_logs，时间夹在两条模型请求中间，读出来要按时间排好。
	if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "cross_group_context", Target: event.MessageID, Message: "retrieval", Metadata: map[string]any{"kind": "group", "group_id": "g", "user_id": "u"}, CreatedAt: base.Add(time.Microsecond)}); err != nil {
		t.Fatal(err)
	}

	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_logs WHERE action = 'debug_trace'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("debug_trace rows in app_logs = %d, want 0", rows)
	}
	path := filepath.Join(dir, debugTraceDirName, base.Format(debugTraceDayForm), debugTraceFileName(event.MessageID))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("trace file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("trace file mode = %v, want 0600", info.Mode().Perm())
	}

	// 进程在写下一条时被杀，文件尾巴上留了半个 gzip 成员；前面完整的记录仍要读得出来。
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0x1f, 0x8b, 0x08, 0x00, 0x01}); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	_, steps, found, err := store.InboundEventDebugTrace(ctx, eventID)
	if err != nil || !found {
		t.Fatalf("trace found=%v err=%v", found, err)
	}
	var got []string
	for _, step := range steps {
		got = append(got, step.Message)
	}
	if strings.Join(got, ",") != "first,retrieval,second" {
		t.Fatalf("steps = %v", got)
	}
	if steps[0].Metadata["request"] != meta["request"] {
		t.Fatal("request payload did not round-trip through the trace file")
	}
}

func TestPruneDebugTraceFilesDropsOnlyWholeExpiredDays(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	root := filepath.Join(dir, debugTraceDirName)
	for _, day := range []string{"2026-09-18", "2026-09-19", "2026-09-20", "not-a-day"} {
		if err := os.MkdirAll(filepath.Join(root, day), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// 截止在 9 月 19 日中午：18 日整天过期，19 日下午的记录还没到期。
	deleted, err := store.PruneDebugTraceFiles(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	entries, _ := os.ReadDir(root)
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	if strings.Join(left, ",") != "2026-09-19,2026-09-20,not-a-day" {
		t.Fatalf("left = %v", left)
	}
	if deleted, err := store.PruneDebugTraceFiles(time.Time{}); err != nil || deleted != 0 {
		t.Fatalf("zero cutoff deleted=%d err=%v", deleted, err)
	}
}

func TestInMemoryStoreKeepsDebugTraceInAppLogs(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "debug_trace", Target: "m", Message: "trace"}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_logs WHERE action = 'debug_trace'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}

func TestPruneCompletedMemoryJobsKeepsPendingAndRecent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -10).UnixNano()
	recent := now.Add(-time.Hour).UnixNano()
	for _, row := range []struct {
		id, status  string
		completedAt any
	}{
		{"old-done", "done", old},
		{"recent-done", "done", recent},
		{"old-pending", "pending", nil},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO memory_jobs (id, kind, session, payload, status, available_at, created_at, updated_at, completed_at)
VALUES (?, 'event', 's', '{}', ?, ?, ?, ?, ?)`, row.id, row.status, old, old, old, row.completedAt); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.PruneCompletedMemoryJobs(ctx, now.AddDate(0, 0, -7))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM memory_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	if strings.Join(left, ",") != "old-pending,recent-done" {
		t.Fatalf("left = %v", left)
	}
}
