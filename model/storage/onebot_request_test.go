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
	items, err := store.ListOneBotRequests(context.Background(), "bot-1", assistant.OneBotRequestPending, 20)
	if err != nil || len(items) != 1 || items[0].Flag != "secret-flag" {
		t.Fatalf("reloaded=%#v err=%v", items, err)
	}
	resolved, err := store.ResolveOneBotRequest(context.Background(), "bot-1", created.ID, assistant.OneBotRequestApproved, "", now.Add(time.Minute))
	if err != nil || resolved.Status != assistant.OneBotRequestApproved || resolved.DecidedAt.IsZero() {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	items, err = store.ListOneBotRequests(context.Background(), "bot-1", assistant.OneBotRequestPending, 20)
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
	items, err := store.ListOneBotRequests(context.Background(), "bot-a", assistant.OneBotRequestPending, 20)
	if err != nil || len(items) != 1 || items[0].ProfileID != "bot-a" {
		t.Fatalf("isolated=%#v err=%v", items, err)
	}
}
