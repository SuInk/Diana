// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 连续超时次数随领取带出来，中间夹一次别的失败就清零；完成后 last_error 留着。
func TestMemoryJobConsecutiveTimeoutsAndLastError(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	store.SetMemoryEventJobDelay(0)
	id, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:1", "u1", "m1", 100))
	if err != nil {
		t.Fatal(err)
	}
	claim := func() assistant.MemoryJob {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `UPDATE memory_jobs SET available_at = 0 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		jobs, err := store.ClaimMemoryJobBatch(ctx, "w", time.Now().Add(time.Minute), 6)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim jobs=%v err=%v", jobs, err)
		}
		return jobs[0]
	}

	job := claim()
	if job.ConsecutiveTimeouts != 0 || job.LastError != "" {
		t.Fatalf("fresh job = %+v", job)
	}
	if err := store.RetryTimedOutMemoryJob(ctx, id, "w", time.Now(), "timeout 1"); err != nil {
		t.Fatal(err)
	}
	job = claim()
	if job.ConsecutiveTimeouts != 1 || job.LastError != "timeout 1" || job.Attempts != 2 {
		t.Fatalf("after one timeout = %+v", job)
	}
	if err := store.RetryTimedOutMemoryJob(ctx, id, "w", time.Now(), "timeout 2"); err != nil {
		t.Fatal(err)
	}
	job = claim()
	if job.ConsecutiveTimeouts != 2 || job.LastError != "timeout 2" {
		t.Fatalf("after two timeouts = %+v", job)
	}
	if err := store.RetryMemoryJob(ctx, id, "w", time.Now(), "bad json"); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNextMemoryJob(ctx, "w", time.Now().Add(time.Minute))
	if err != nil || !ok || job.ConsecutiveTimeouts != 0 || job.LastError != "bad json" {
		t.Fatalf("other failure should reset the streak: job=%+v ok=%v err=%v", job, ok, err)
	}

	if err := store.CompleteMemoryJob(ctx, id, "w"); err != nil {
		t.Fatal(err)
	}
	var status string
	var lastError sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT status, last_error FROM memory_jobs WHERE id = ?`, id).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "done" || lastError.String != "bad json" {
		t.Fatalf("completed job status=%s last_error=%q", status, lastError.String)
	}
	if _, ok, err := store.ClaimNextMemoryJob(ctx, "w", time.Now().Add(time.Minute)); err != nil || ok {
		t.Fatalf("completed job with last_error was claimable ok=%v err=%v", ok, err)
	}
}

// 老库没有连续超时列，启动时要补上，已有任务从 0 开始数。
func TestMemoryJobTimeoutColumnAddedToOldDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.SetMemoryEventJobDelay(0)
	id, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:1", "u1", "m1", 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE memory_jobs DROP COLUMN consecutive_timeouts`); err != nil {
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
	if has, err := store.hasColumn("memory_jobs", "consecutive_timeouts"); err != nil || !has {
		t.Fatalf("column restored=%v err=%v", has, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE memory_jobs SET available_at = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNextMemoryJob(ctx, "w", time.Now().Add(time.Minute))
	if err != nil || !ok || job.ID != id || job.ConsecutiveTimeouts != 0 {
		t.Fatalf("claim after upgrade job=%+v ok=%v err=%v", job, ok, err)
	}
}
