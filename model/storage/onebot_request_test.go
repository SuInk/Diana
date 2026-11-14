// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestOneBotRequestPersistsDeduplicatesAndResolves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onebot-request.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_788_300_000, 0)
	request := assistant.OneBotRequestRecord{
		ProfileID: "bot-1", Platform: assistant.PlatformOneBotV11, SelfID: "42",
		RequestType: "group", SubType: "invite", UserID: "10001", GroupID: "20002",
		Comment: "join us", Flag: "secret-flag", Status: assistant.OneBotRequestPending,
		CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := store.SaveOneBotRequest(context.Background(), request)
	if err != nil || !inserted || created.ID == "" {
		t.Fatalf("created=%#v inserted=%v err=%v", created, inserted, err)
	}
	duplicate, inserted, err := store.SaveOneBotRequest(context.Background(), request)
	if err != nil || inserted || duplicate.ID != created.ID {
		t.Fatalf("duplicate=%#v inserted=%v err=%v", duplicate, inserted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	items, err := store.ListOneBotRequests(context.Background(), assistant.OneBotRequestQuery{ProfileID: "bot-1", Status: assistant.OneBotRequestPending, Limit: 20})
	if err != nil || len(items) != 1 || items[0].Flag != "secret-flag" {
		t.Fatalf("reloaded=%#v err=%v", items, err)
	}
	resolved, err := store.ResolveOneBotRequest(context.Background(), "bot-1", created.ID, assistant.OneBotRequestApproved, "", now.Add(time.Minute))
	if err != nil || resolved.Status != assistant.OneBotRequestApproved || resolved.DecidedAt.IsZero() {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	items, err = store.ListOneBotRequests(context.Background(), assistant.OneBotRequestQuery{ProfileID: "bot-1", Status: assistant.OneBotRequestPending, Limit: 20})
	if err != nil || len(items) != 0 {
		t.Fatalf("pending after resolve=%#v err=%v", items, err)
	}
	resolvedAgain, err := store.ResolveOneBotRequest(context.Background(), "bot-1", created.ID, assistant.OneBotRequestApproved, "", now.Add(2*time.Minute))
	if err != nil || resolvedAgain.Status != assistant.OneBotRequestApproved {
		t.Fatalf("idempotent resolve=%#v err=%v", resolvedAgain, err)
	}
}

func TestOneBotRequestProfileIsolation(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "onebot-request-isolation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()
	for _, profileID := range []string{"bot-a", "bot-b"} {
		_, _, err := store.SaveOneBotRequest(context.Background(), assistant.OneBotRequestRecord{
			ProfileID: profileID, Platform: assistant.PlatformOneBotV11, RequestType: "friend",
			UserID: "10001", Flag: "same-platform-flag", Status: assistant.OneBotRequestPending,
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListOneBotRequests(context.Background(), assistant.OneBotRequestQuery{ProfileID: "bot-a", Status: assistant.OneBotRequestPending, Limit: 20})
	if err != nil || len(items) != 1 || items[0].ProfileID != "bot-a" {
		t.Fatalf("isolated=%#v err=%v", items, err)
	}
}

// TestListOneBotRequestsKeysetSurvivesResolve 按 (created_at, id) 键集翻页：limit<=0
// 给全部；翻页之间把已经看过的请求处理掉，后面的页不跳条、不重复。同一时刻的请求按 id 分先后。
func TestListOneBotRequestsKeysetSurvivesResolve(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "onebot-request-keyset.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	base := time.Unix(1_788_300_000, 0)
	for i := range 130 {
		// 每三条共用一个时间戳，逼游标靠 id 区分同一时刻的请求。
		at := base.Add(time.Duration(i/3) * time.Second)
		request := assistant.OneBotRequestRecord{
			ProfileID: "bot-1", Platform: assistant.PlatformOneBotV11, SelfID: "42",
			RequestType: "friend", UserID: strconv.Itoa(10000 + i), Flag: "flag-" + strconv.Itoa(i),
			Status: assistant.OneBotRequestPending, CreatedAt: at, UpdatedAt: at,
		}
		if _, _, err := store.SaveOneBotRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	query := assistant.OneBotRequestQuery{ProfileID: "bot-1", Status: assistant.OneBotRequestPending}
	all, err := store.ListOneBotRequests(ctx, query)
	if err != nil || len(all) != 130 {
		t.Fatalf("got %d err=%v", len(all), err)
	}
	if count, err := store.CountOneBotRequests(ctx, query); err != nil || count != 130 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	seen := map[string]bool{}
	query.Limit = 40
	for page := 0; ; page++ {
		items, err := store.ListOneBotRequests(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("第 %d 页重复给了 %s", page, item.ID)
			}
			seen[item.ID] = true
			// 看过就处理掉：它离开待处理列表，后面的页不能因此前移跳条。
			if _, err := store.ResolveOneBotRequest(ctx, "bot-1", item.ID, assistant.OneBotRequestApproved, "", base.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		last := items[len(items)-1]
		query.Before = &assistant.KeysetCursor{At: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 130 {
		t.Fatalf("翻完只看到 %d 条，跳过了 %d 条", len(seen), 130-len(seen))
	}
}
