// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSQLiteStoreListUserMemoriesPagingAndSearch(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for i := 0; i < 5; i++ {
		event := assistant.MessageEvent{
			Kind:       assistant.EventKindGroup,
			GroupID:    "20001",
			UserID:     fmt.Sprintf("1000%d", i),
			SenderName: fmt.Sprintf("成员%d", i),
			MessageID:  fmt.Sprintf("m%d", i),
			RawMessage: fmt.Sprintf("第 %d 条发言内容", i),
			Time:       1_700_000_000 + int64(i),
		}
		if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{}); err != nil {
			t.Fatal(err)
		}
	}

	profiles, total, err := store.ListUserMemories(ctx, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(profiles) != 2 {
		t.Fatalf("total=%d page=%d, want 5/2", total, len(profiles))
	}

	page2, _, err := store.ListUserMemories(ctx, "", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].UserID == profiles[0].UserID {
		t.Fatalf("page2=%#v overlaps page1=%#v", page2, profiles)
	}

	matched, total, err := store.ListUserMemories(ctx, "成员3", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(matched) != 1 || matched[0].DisplayName != "成员3" {
		t.Fatalf("matched=%#v total=%d, want only 成员3", matched, total)
	}

	// LIKE 通配符按字面处理，不能变成全量匹配。
	wild, total, err := store.ListUserMemories(ctx, "%", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(wild) != 0 {
		t.Fatalf("wildcard query matched %d rows, want 0", total)
	}
}
