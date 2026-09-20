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

func newPendingDirectMessageStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pending-direct.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPendingDirectMessagesSurviveAndAreTakenOnce(t *testing.T) {
	store := newPendingDirectMessageStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, text := range []string{"第一条", "第二条"} {
		if _, err := store.SavePendingDirectMessage(ctx, assistant.PendingDirectMessage{
			ProfileID: "a", Platform: "onebot-v11", UserID: "555", SourceSession: "group:123",
			Message: text, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 别人的和过期的都不该被算进来，也不该被取走。
	if _, err := store.SavePendingDirectMessage(ctx, assistant.PendingDirectMessage{
		ProfileID: "a", UserID: "666", Message: "别人的", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SavePendingDirectMessage(ctx, assistant.PendingDirectMessage{
		ProfileID: "a", UserID: "555", Message: "过期的", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	count, err := store.CountPendingDirectMessages(ctx, "a", "555", now)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("没过期的条数 = %d，期望 2", count)
	}

	items, err := store.TakePendingDirectMessages(ctx, "a", "555", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Message != "第一条" || items[1].Message != "第二条" {
		t.Fatalf("取出的内容不对：%#v", items)
	}
	if items[0].SourceSession != "group:123" || items[0].Platform != "onebot-v11" {
		t.Fatalf("来历没有存住：%#v", items[0])
	}

	// 取出即删除：好友通知和主人审批前后脚到达时，同一段话只能发一遍。
	again, err := store.TakePendingDirectMessages(ctx, "a", "555", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("第二次又取出了 %d 条", len(again))
	}
	// 别人的那条不受影响。
	if count, err := store.CountPendingDirectMessages(ctx, "a", "666", now); err != nil || count != 1 {
		t.Fatalf("别人的托管被连坐了：count=%d err=%v", count, err)
	}
}

func TestPendingDirectMessagesPurgeExpired(t *testing.T) {
	store := newPendingDirectMessageStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := store.SavePendingDirectMessage(ctx, assistant.PendingDirectMessage{
		ProfileID: "a", UserID: "555", Message: "过期的", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SavePendingDirectMessage(ctx, assistant.PendingDirectMessage{
		ProfileID: "a", UserID: "555", Message: "还在的", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PurgeExpiredPendingDirectMessages(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("清理了 %d 条，期望 1", removed)
	}
	if count, err := store.CountPendingDirectMessages(ctx, "a", "555", now); err != nil || count != 1 {
		t.Fatalf("清理把没过期的也删了：count=%d err=%v", count, err)
	}
}

func TestPendingDirectMessagesSchema(t *testing.T) {
	store := newPendingDirectMessageStore(t)
	for _, name := range []string{"pending_direct_messages", "idx_pending_direct_messages_target"} {
		var found string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, name).Scan(&found); err != nil {
			t.Fatalf("schema object %q missing: %v", name, err)
		}
	}
}
