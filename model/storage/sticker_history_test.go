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

// 原贴纸 ID 要跟着入库；机器人自己发出去的那条不带 ID，不能把它冲掉。加列之前入库的旧行
// 升级时从原消息补上。
func TestStickerAssetsKeepTelegramStickerFileID(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "stickers.db")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "media.webp")
	if err := os.WriteFile(path, []byte("thumb"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("2", 64)
	segment := func(fileID string) []assistant.MessageSegment {
		data := map[string]string{"sub_type": "telegram_sticker", "summary": "😭", "cached_file": path, "content_sha256": hash}
		if fileID != "" {
			data["sticker_file_id"] = fileID
		}
		return []assistant.MessageSegment{{Type: "image", Data: data}}
	}
	session := "bot-a:group:-10012345"
	base := assistant.MessageEvent{Kind: assistant.EventKindGroup, ContextNamespace: "bot-a", ProfileID: "profile-a", GroupID: "-10012345"}
	incoming := base
	incoming.MessageID, incoming.Time, incoming.Segments = "in", 10, segment("CAACAgUAAx0")
	if err := store.AppendMessageEvent(ctx, session, incoming); err != nil {
		t.Fatal(err)
	}
	outgoing := base
	outgoing.MessageID, outgoing.Time, outgoing.Segments = "out", 20, segment("")
	if err := store.AppendMessageEvent(ctx, session, outgoing); err != nil {
		t.Fatal(err)
	}
	list := func() []assistant.StickerAsset {
		t.Helper()
		assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: session, Limit: 10})
		if err != nil || len(assets) != 1 {
			t.Fatalf("assets=%#v err=%v", assets, err)
		}
		return assets
	}
	if got := list()[0]; got.StickerFileID != "CAACAgUAAx0" || got.MessageID != "out" {
		t.Fatalf("asset=%#v", got)
	}

	// 模拟加列之前的旧库：ID 为空、回填标记没有，重开后从原消息补回来。
	if _, err := store.db.Exec(`UPDATE sticker_assets SET sticker_file_id = '', message_id = 'in', segment_index = 0; DELETE FROM app_state WHERE key = ?`, stickerFileIDBackfillKey); err != nil {
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
	if got := list()[0]; got.StickerFileID != "CAACAgUAAx0" {
		t.Fatalf("backfilled asset=%#v", got)
	}
}

// 历史行按哈希单查表情包标注：标过的原样读回（版本一并带回，动图旧标注由调用方判断），
// 没标过和哈希不合法都当没有。
func TestGetStickerTagsByHash(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "stickers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	hash := strings.Repeat("3", 64)
	if _, found, err := store.GetStickerTags(ctx, hash); err != nil || found {
		t.Fatalf("untagged sticker: found=%v err=%v", found, err)
	}
	if err := store.SaveStickerTags(ctx, assistant.StickerTagRecord{
		ContentSHA256: hash, Gist: " 猫猫歪头装傻 ", Tags: []string{"装傻", "疑惑"}, Category: "动物", Version: assistant.StickerAnnotationVersion,
	}); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.GetStickerTags(ctx, strings.ToUpper(hash[:1])+hash[1:])
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if record.ContentSHA256 != hash || record.Gist != "猫猫歪头装傻" || strings.Join(record.Tags, ",") != "装傻,疑惑" ||
		record.Category != "动物" || record.Version != assistant.StickerAnnotationVersion {
		t.Fatalf("record=%#v", record)
	}
	if _, found, err := store.GetStickerTags(ctx, "not-a-hash"); err != nil || found {
		t.Fatalf("invalid hash: found=%v err=%v", found, err)
	}
}
