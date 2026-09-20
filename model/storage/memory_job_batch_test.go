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

func batchJob(session, userID, messageID string, at int64) assistant.MemoryJobPayload {
	return assistant.MemoryJobPayload{Kind: assistant.MemoryJobEvent, Session: session, Event: assistant.MessageEvent{
		Kind:      assistant.EventKindGroup,
		GroupID:   "123",
		UserID:    userID,
		MessageID: messageID,
		Time:      at,
		Segments:  []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": messageID}}},
	}}
}

// 攒批只能把同会话同发言者的事件任务凑在一起：别人的消息混进来会让候选的归属出错。
func TestClaimMemoryJobBatchGroupsBySessionAndSpeaker(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	store.SetMemoryEventJobDelay(0)

	for _, payload := range []assistant.MemoryJobPayload{
		batchJob("group:123", "alice", "m1", 100),
		batchJob("group:123", "bob", "m2", 200),
		batchJob("group:123", "alice", "m3", 300),
		batchJob("group:456", "alice", "m4", 400),
	} {
		if _, _, err := store.EnqueueMemoryJob(ctx, payload); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("claimed = %d, want 2 (alice 在 group:123 的两条)", len(jobs))
	}
	for _, job := range jobs {
		if job.Payload.Event.UserID != "alice" || job.Payload.Session != "group:123" {
			t.Fatalf("批次里混进了别人的任务：%#v", job.Payload.Event)
		}
		if job.Attempts != 1 {
			t.Fatalf("attempts = %d", job.Attempts)
		}
	}

	// 剩下的两条仍可被领走，且不会跟上一批重复。
	next, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Payload.Event.UserID != "bob" {
		t.Fatalf("second batch = %#v", next)
	}
}

// 摘要任务形状不同，永远单独处理。
func TestClaimMemoryJobBatchKeepsSummaryAlone(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	store.SetMemoryEventJobDelay(0)

	if _, _, err := store.EnqueueMemoryJob(ctx, assistant.MemoryJobPayload{
		Kind:    assistant.MemoryJobSummary,
		Session: "group:123",
		Events:  []assistant.MessageEvent{{MessageID: "s1", Time: 10}, {MessageID: "s2", Time: 20}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:123", "alice", "m1", 100)); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Payload.Kind != assistant.MemoryJobSummary {
		t.Fatalf("jobs = %#v", jobs)
	}
}

// 默认要有攒批窗口：入队之后不能立刻被领走。
func TestEventMemoryJobsWaitForBatchWindow(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:123", "alice", "m1", 100)); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("攒批窗口内不该被领走：%#v", jobs)
	}
	store.SetMemoryEventJobDelay(0)
	if _, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:123", "alice", "m2", 200)); err != nil {
		t.Fatal(err)
	}
	jobs, err = store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Payload.Event.MessageID != "m2" {
		t.Fatalf("jobs = %#v", jobs)
	}
}
