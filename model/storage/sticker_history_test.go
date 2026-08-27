// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestListRecentStickerEventsRespectsSeparateGroupAndPrivateSwitches(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "stickers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	events := []struct {
		session string
		event   assistant.MessageEvent
	}{
		{"bot-a:group:one", assistant.MessageEvent{Kind: assistant.EventKindGroup, ContextNamespace: "bot-a", ProfileID: "profile-a", GroupID: "one", MessageID: "one", Time: 1}},
		{"bot-a:group:two", assistant.MessageEvent{Kind: assistant.EventKindGroup, ContextNamespace: "bot-a", ProfileID: "profile-a", GroupID: "two", MessageID: "two", Time: 2}},
		{"bot-a:private:user", assistant.MessageEvent{Kind: assistant.EventKindPrivate, ContextNamespace: "bot-a", ProfileID: "profile-a", UserID: "user", MessageID: "private", Time: 3}},
		{"bot-b:group:three", assistant.MessageEvent{Kind: assistant.EventKindGroup, ContextNamespace: "bot-b", ProfileID: "profile-b", GroupID: "three", MessageID: "foreign", Time: 4}},
	}
	for _, item := range events {
		if err := store.AppendMessageEvent(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}

	current, err := store.ListRecentStickerEvents(ctx, assistant.StickerHistoryQuery{Session: "bot-a:group:one", Limit: 20})
	if err != nil || len(current) != 1 || current[0].MessageID != "one" {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	shared, err := store.ListRecentStickerEvents(ctx, assistant.StickerHistoryQuery{
		Session: "bot-a:group:one", ContextNamespace: "bot-a", ProfileID: "profile-a", ShareGroups: true, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 2 || shared[0].MessageID != "one" || shared[1].MessageID != "two" {
		t.Fatalf("shared=%#v", shared)
	}
	privateShared, err := store.ListRecentStickerEvents(ctx, assistant.StickerHistoryQuery{
		Session: "bot-a:group:one", ContextNamespace: "bot-a", ProfileID: "profile-a", SharePrivate: true, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(privateShared) != 2 || privateShared[0].MessageID != "one" || privateShared[1].MessageID != "private" {
		t.Fatalf("private shared=%#v", privateShared)
	}
}
