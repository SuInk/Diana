// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func stickerLibraryEvent(profile, group, messageID string, at int64, hash, summary, path string) assistant.MessageEvent {
	return assistant.MessageEvent{
		ProfileID: profile, Kind: assistant.EventKindGroup, GroupID: group, UserID: "u", MessageID: messageID, Time: at,
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{
			"summary": summary, "sub_type": "1", "cached_file": path, "content_sha256": hash, "cached_mime": "image/gif",
		}}},
	}
}

// 控制台浏览表情包池：同一张图在多个群出现只列一次、取最近那次；简介跟着带出来；
// 按机器人筛选和按名称/简介搜索都要生效。
func TestListStickerLibrary(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	shared := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	foreign := strings.Repeat("c", 64)
	for _, item := range []struct {
		session string
		event   assistant.MessageEvent
	}{
		{"group:g1", stickerLibraryEvent("bot", "g1", "m1", 100, shared, "[懂了]", "/cache/old.gif")},
		{"group:g2", stickerLibraryEvent("bot", "g2", "m2", 300, shared, "[懂了]", "/cache/new.gif")},
		{"group:g1", stickerLibraryEvent("bot", "g1", "m3", 200, other, "[无语]", "/cache/other.gif")},
		{"group:g9", stickerLibraryEvent("other-bot", "g9", "m9", 400, foreign, "[别人的]", "/cache/foreign.gif")},
	} {
		if err := store.indexStickerAssets(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveImageDescription(ctx, assistant.ImageDescriptionRecord{ContentSHA256: other, Description: "面无表情，表达无奈", Source: "vision"}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("page = %#v", page)
	}
	first, second := page.Items[0], page.Items[1]
	if first.Hash != shared || first.Sessions != 2 || first.GroupID != "g2" || first.Summary != "懂了" {
		t.Fatalf("first = %#v", first)
	}
	if second.Hash != other || second.Description != "面无表情，表达无奈" {
		t.Fatalf("second = %#v", second)
	}

	page, err = store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", Search: "无奈"})
	if err != nil || page.Total != 1 || page.Items[0].Hash != other {
		t.Fatalf("search by description = %#v err=%v", page, err)
	}
	page, err = store.ListStickerLibrary(ctx, StickerLibraryQuery{})
	if err != nil || page.Total != 3 {
		t.Fatalf("all bots = %#v err=%v", page, err)
	}
	page, err = store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", Limit: 1, Offset: 1})
	if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Items[0].Hash != other {
		t.Fatalf("paging = %#v err=%v", page, err)
	}

	path, found, err := store.StickerAssetFile(ctx, shared, "bot")
	if err != nil || !found || path != "/cache/new.gif" {
		t.Fatalf("file = %q found=%v err=%v", path, found, err)
	}
	if _, found, _ := store.StickerAssetFile(ctx, foreign, "bot"); found {
		t.Fatal("another bot's sticker must not be readable under this bot's scope")
	}
	if _, found, _ := store.StickerAssetFile(ctx, "../etc/passwd", ""); found {
		t.Fatal("invalid hash must not match")
	}
}

// 资产查询带出简介、标签和本会话的发送记录；超上限时按「最后一次用到」淘汰，
// 机器人发过也算用到；按标签能在控制台搜到。
func TestStickerAssetsCarryTagsUsageAndPrune(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	oldest, middle, newest := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	for _, item := range []struct {
		hash string
		at   int64
	}{{oldest, 100}, {middle, 200}, {newest, 300}} {
		if err := store.indexStickerAssets(ctx, "group:g1", stickerLibraryEvent("bot", "g1", "m"+item.hash[:1], item.at, item.hash, "[动画表情]", "/cache/"+item.hash[:1]+".gif")); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveImageDescription(ctx, assistant.ImageDescriptionRecord{ContentSHA256: middle, Description: "通用描述", Source: "vision"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStickerTags(ctx, assistant.StickerTagRecord{ContentSHA256: newest, Gist: "摸头安慰", Tags: []string{"安慰", "摸头"}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.RecordStickerSent(ctx, "group:g1", oldest, 1000); err != nil {
			t.Fatal(err)
		}
	}
	// 别的会话发过不算本会话的发送记录。
	if err := store.RecordStickerSent(ctx, "group:g2", middle, 2000); err != nil {
		t.Fatal(err)
	}

	assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "group:g1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byHash := map[string]assistant.StickerAsset{}
	for _, asset := range assets {
		byHash[asset.ContentSHA256] = asset
	}
	if got := byHash[newest]; !got.Tagged || got.Gist != "摸头安慰" || strings.Join(got.Tags, "|") != "安慰|摸头" {
		t.Fatalf("tagged asset = %#v", got)
	}
	if got := byHash[middle]; got.Tagged || got.Description != "通用描述" || got.LastSentAt != 0 {
		t.Fatalf("described asset = %#v", got)
	}
	if got := byHash[oldest]; got.SentCount != 2 || got.LastSentAt != 1000 {
		t.Fatalf("sent asset = %#v", got)
	}

	removed, err := store.PruneStickerAssets(ctx, "group:g1", 2)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	assets, err = store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "group:g1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 || assets[0].ContentSHA256 == middle || assets[1].ContentSHA256 == middle {
		t.Fatalf("after prune = %#v", assets)
	}
	if removed, err := store.PruneStickerAssets(ctx, "group:g1", 2); err != nil || removed != 0 {
		t.Fatalf("second prune removed=%d err=%v", removed, err)
	}

	page, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{Search: "摸头"})
	if err != nil || page.Total != 1 || page.Items[0].Hash != newest || page.Items[0].Description != "摸头安慰" {
		t.Fatalf("library search = %#v err=%v", page, err)
	}
}
