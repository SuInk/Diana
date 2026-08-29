// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestStickerAssetsKeepCurrentConversationQuotaSeparateFromSharedTraffic(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "stickers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	dir := t.TempDir()
	appendSticker := func(session string, event assistant.MessageEvent, hash string) {
		path := filepath.Join(dir, hash[:8]+".gif")
		if err := os.WriteFile(path, []byte(hash), 0o600); err != nil {
			t.Fatal(err)
		}
		event.Segments = []assistant.MessageSegment{{Type: "image", Data: map[string]string{
			"sub_type": "1", "cached_file": path, "content_sha256": hash,
		}}}
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}

	currentHash := strings.Repeat("a", 64)
	appendSticker("bot-a:private:user", assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, ContextNamespace: "bot-a", ProfileID: "profile-a",
		UserID: "user", MessageID: "current-old", Time: 1,
	}, currentHash)
	for index, hashChar := range []string{"b", "c", "d", "e", "f"} {
		appendSticker("bot-a:group:busy", assistant.MessageEvent{
			Kind: assistant.EventKindGroup, ContextNamespace: "bot-a", ProfileID: "profile-a",
			GroupID: "busy", MessageID: "shared-" + hashChar, Time: int64(100 + index),
		}, strings.Repeat(hashChar, 64))
	}

	assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{
		Session: "bot-a:private:user", ContextNamespace: "bot-a", ProfileID: "profile-a",
		ShareGroups: true, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 || assets[0].MessageID != "current-old" {
		t.Fatalf("assets=%#v", assets)
	}
	if assets[0].Summary != "动画表情" || assets[0].ContentSHA256 != currentHash {
		t.Fatalf("current asset=%#v", assets[0])
	}
}

func TestStickerAssetBackfillRecognizesPlatformSubtypeWithoutSummary(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "stickers.db")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.gif")
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("1", 64)
	event := assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, ContextNamespace: "bot-a", ProfileID: "profile-a",
		UserID: "user", MessageID: "legacy", Time: 10,
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{
			"sub_type": "1", "cached_file": path, "content_sha256": hash,
		}}},
	}
	if err := store.AppendMessageEvent(ctx, "bot-a:private:user", event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM sticker_assets; DELETE FROM app_state WHERE key = ?`, stickerAssetsBackfillKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "bot-a:private:user", Limit: 10})
	if err != nil || len(assets) != 1 || assets[0].ContentSHA256 != hash {
		t.Fatalf("backfilled assets=%#v err=%v", assets, err)
	}
}
