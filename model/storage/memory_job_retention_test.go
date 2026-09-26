// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 只删早于截止时间完成的任务；排队中、处理中和刚做完的任务一条都不能动。
func TestPruneMemoryJobsDeletesOnlyOldCompletedJobs(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	ids := map[string]string{}
	for _, name := range []string{"old_done", "recent_done", "pending", "processing"} {
		id, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:123", "alice", name, 100))
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	now := time.Now().UTC()
	for name, state := range map[string]struct {
		status      string
		completedAt any
	}{
		"old_done":    {"done", now.Add(-8 * 24 * time.Hour).UnixNano()},
		"recent_done": {"done", now.Add(-time.Hour).UnixNano()},
		"processing":  {"processing", nil},
	} {
		if _, err := store.db.Exec(`UPDATE memory_jobs SET status = ?, completed_at = ? WHERE id = ?`, state.status, state.completedAt, ids[name]); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := store.PruneMemoryJobs(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	for name, id := range ids {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_jobs WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 0, false: 1}[name == "old_done"]; count != want {
			t.Fatalf("%s rows = %d, want %d", name, count, want)
		}
	}

	if deleted, err := store.PruneMemoryJobs(ctx, time.Time{}); err != nil || deleted != 0 {
		t.Fatalf("zero cutoff must be a no-op: deleted=%d err=%v", deleted, err)
	}
}

// 超过一批的量要分批删干净，不能只删掉第一批就停。
func TestPruneMemoryJobsDeletesAcrossBatches(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	old := time.Now().Add(-30 * 24 * time.Hour).UnixNano()
	if _, err := store.db.Exec(`
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 1203)
INSERT INTO memory_jobs (id, kind, session, payload, status, attempts, available_at, created_at, updated_at, completed_at)
SELECT 'job-' || i, 'event', 'group:1', '{}', 'done', 1, ?, ?, ?, ? FROM n
`, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.PruneMemoryJobs(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1203 {
		t.Fatalf("deleted = %d, want 1203", deleted)
	}
}
