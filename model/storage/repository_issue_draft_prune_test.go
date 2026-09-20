// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 过期满保留期的待审批草稿会被整条删掉；没到期的、以及已创建/已取消这类留痕不动。
func TestPruneRepositoryIssueDrafts(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	drafts := []assistant.RepositoryIssueDraft{
		{ID: "purge-me", GroupID: "g", Repository: "acme/demo", Status: "pending",
			CreatedAt: now.Add(-60 * 24 * time.Hour), UpdatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "still-expired", GroupID: "g", Repository: "acme/demo", Status: "pending",
			CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: now.Add(-10 * 24 * time.Hour)},
		{ID: "fresh", GroupID: "g", Repository: "acme/demo", Status: "pending",
			CreatedAt: now, UpdatedAt: now},
		{ID: "old-created", GroupID: "g", Repository: "acme/demo", Status: "created", IssueNumber: 7,
			CreatedAt: now.Add(-90 * 24 * time.Hour), UpdatedAt: now.Add(-90 * 24 * time.Hour)},
		{ID: "old-cancelled", GroupID: "g", Repository: "acme/demo", Status: "cancelled",
			CreatedAt: now.Add(-90 * 24 * time.Hour), UpdatedAt: now.Add(-90 * 24 * time.Hour)},
	}
	for _, draft := range drafts {
		if err := store.SaveRepositoryIssueDraft(ctx, draft); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := store.PruneRepositoryIssueDrafts(ctx, assistant.RepositoryIssueDraftPurgeCutoff(now))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d，只应删掉过期满保留期的那一条", deleted)
	}
	remaining, err := store.ListRepositoryIssueDrafts(ctx, "", "all")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, draft := range remaining {
		kept[draft.ID] = true
	}
	if kept["purge-me"] {
		t.Fatal("过期满保留期的草稿没被删掉")
	}
	for _, id := range []string{"still-expired", "fresh", "old-created", "old-cancelled"} {
		if !kept[id] {
			t.Fatalf("%s 不该被删掉", id)
		}
	}
	// 零值截止时间表示不清理，避免调用方漏传时把整张表删空。
	if count, err := store.PruneRepositoryIssueDrafts(ctx, time.Time{}); err != nil || count != 0 {
		t.Fatalf("零值截止时间不该删任何东西：count=%d err=%v", count, err)
	}
}

// 删除只删指定那条，别的草稿不受影响。
func TestDeleteRepositoryIssueDraft(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"keep", "drop"} {
		if err := store.SaveRepositoryIssueDraft(ctx, assistant.RepositoryIssueDraft{
			ID: id, GroupID: "g", Repository: "acme/demo", Status: "pending", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.DeleteRepositoryIssueDraft(ctx, "drop")
	if err != nil || !deleted {
		t.Fatalf("删除失败：deleted=%v err=%v", deleted, err)
	}
	if deleted, err := store.DeleteRepositoryIssueDraft(ctx, "drop"); err != nil || deleted {
		t.Fatalf("重复删除应当返回 false：deleted=%v err=%v", deleted, err)
	}
	remaining, err := store.ListRepositoryIssueDrafts(ctx, "", "all")
	if err != nil || len(remaining) != 1 || remaining[0].ID != "keep" {
		t.Fatalf("剩下的草稿不对：%#v err=%v", remaining, err)
	}
}
