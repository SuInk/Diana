// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := store.SaveStickerTags(ctx, assistant.StickerTagRecord{ContentSHA256: newest, Gist: "摸头安慰", Tags: []string{"安慰", "摸头"}, Version: assistant.StickerAnnotationVersion}); err != nil {
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
	// 别的会话发过的单独带出来，跨会话共享时按它降权。
	if got := byHash[middle]; got.Tagged || got.Description != "通用描述" || got.LastSentAt != 0 || got.ElsewhereLastSentAt != 2000 {
		t.Fatalf("described asset = %#v", got)
	}
	if got := byHash[oldest]; got.SentCount != 2 || got.LastSentAt != 1000 || got.ElsewhereLastSentAt != 0 {
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

// 引入命名空间之前落库的表情包用的是旧键（group:…），它们属于同一个群：
// 检索时要当成本会话的库存，按上限淘汰时两种键合在一起算。
func TestStickerAssetsIncludeLegacySessionKey(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	legacy, current, other := strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64)
	if err := store.indexStickerAssets(ctx, "group:g1", stickerLibraryEvent("", "g1", "old", 100, legacy, "[旧的]", "/cache/old.gif")); err != nil {
		t.Fatal(err)
	}
	if err := store.indexStickerAssets(ctx, "ns:group:g1", stickerLibraryEvent("bot", "g1", "new", 200, current, "[新的]", "/cache/new.gif")); err != nil {
		t.Fatal(err)
	}
	if err := store.indexStickerAssets(ctx, "group:g2", stickerLibraryEvent("", "g2", "x", 300, other, "[别的群]", "/cache/x.gif")); err != nil {
		t.Fatal(err)
	}
	assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "ns:group:g1", ContextNamespace: "ns", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 || assets[0].ContentSHA256 != current || assets[1].ContentSHA256 != legacy || assets[1].Session != "ns:group:g1" {
		t.Fatalf("assets = %#v", assets)
	}
	removed, err := store.PruneStickerAssets(ctx, "ns:group:g1", 1)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	assets, err = store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "ns:group:g1", ContextNamespace: "ns", Limit: 100})
	if err != nil || len(assets) != 1 || assets[0].ContentSHA256 != current {
		t.Fatalf("after prune = %#v err=%v", assets, err)
	}
}

// 人设判断按「人设指纹 + 图片」存取，人设不同互不影响，重判会覆盖。
func TestStickerPersonaFitRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-persona.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	a, b := strings.Repeat("7", 64), strings.Repeat("8", 64)
	if err := store.SaveStickerPersonaFit(ctx, "diana", a, false, "大叔口吻"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStickerPersonaFit(ctx, "diana", b, true, ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.StickerPersonaFit(ctx, "diana", []string{a, b, strings.Repeat("9", 64)})
	if err != nil || len(got) != 2 || got[a] || !got[b] {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if other, _ := store.StickerPersonaFit(ctx, "miku", []string{a}); len(other) != 0 {
		t.Fatalf("verdict leaked across personas: %v", other)
	}
	if err := store.SaveStickerPersonaFit(ctx, "diana", a, true, ""); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.StickerPersonaFit(ctx, "diana", []string{a}); !again[a] {
		t.Fatal("re-judgement did not overwrite")
	}
}

// GIF 以前的标注只看了第一帧：版本不对的 GIF 标签按没标注处理，等检索时重标；静态图的旧标签照用。
func TestStickerTagsFromBeforeGIFStoryboardAreStaleForGIFsOnly(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-tag-version.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	gifHash, pngHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, item := range []struct{ hash, path string }{{gifHash, "/cache/x.gif"}, {pngHash, "/cache/y.png"}} {
		event := stickerLibraryEvent("bot", "g1", "m-"+item.hash[:1], 100, item.hash, "[动画表情]", item.path)
		if strings.HasSuffix(item.path, ".png") {
			event.Segments[0].Data["cached_mime"] = "image/png"
		}
		if err := store.indexStickerAssets(ctx, "group:g1", event); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveStickerTags(ctx, assistant.StickerTagRecord{ContentSHA256: item.hash, Gist: "旧标注", Tags: []string{"睡觉"}}); err != nil {
			t.Fatal(err)
		}
	}
	tagged := func() map[string]assistant.StickerAsset {
		assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "group:g1", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]assistant.StickerAsset{}
		for _, asset := range assets {
			out[asset.ContentSHA256] = asset
		}
		return out
	}
	got := tagged()
	if got[gifHash].Tagged || got[gifHash].Gist != "" || len(got[gifHash].Tags) != 0 {
		t.Fatalf("stale gif tags still used: %#v", got[gifHash])
	}
	if !got[pngHash].Tagged || got[pngHash].Gist != "旧标注" {
		t.Fatalf("static image tags dropped: %#v", got[pngHash])
	}
	if err := store.SaveStickerTags(ctx, assistant.StickerTagRecord{ContentSHA256: gifHash, Gist: "趴在床上扭动", Tags: []string{"扭动"}, Version: assistant.StickerAnnotationVersion}); err != nil {
		t.Fatal(err)
	}
	if again := tagged()[gifHash]; !again.Tagged || again.Gist != "趴在床上扭动" {
		t.Fatalf("re-annotated gif = %#v", again)
	}
}

func stickerPrivateEvent(profile, user, messageID string, at int64, hash, summary, path string) assistant.MessageEvent {
	event := stickerLibraryEvent(profile, "", messageID, at, hash, summary, path)
	event.Kind, event.UserID = assistant.EventKindPrivate, user
	return event
}

// 控制台的分类栏和筛选：画风大类、来源、关键词、多久没用、机器人发没发过，各条件叠加生效；
// 分类栏每一栏按「只放开这一栏」计数；排序可以把最久没用的排前面。
func TestStickerLibraryFacetsAndFilters(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-facets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().Unix()
	day := int64(24 * 3600)
	anime := strings.Repeat("a", 64)
	cat := strings.Repeat("b", 64)
	oldText := strings.Repeat("c", 64)
	plain := strings.Repeat("d", 64)
	foreign := strings.Repeat("e", 64)
	for _, item := range []struct {
		session string
		event   assistant.MessageEvent
	}{
		{"group:g1", stickerLibraryEvent("bot", "g1", "m1", now-day, anime, "[害羞]", "/cache/anime.gif")},
		{"group:g2", stickerLibraryEvent("bot", "g2", "m2", now-2*day, anime, "[害羞]", "/cache/anime.gif")},
		{"group:g1", stickerLibraryEvent("bot", "g1", "m3", now-3*day, cat, "[无语]", "/cache/cat.gif")},
		{"private:u1", stickerPrivateEvent("bot", "u1", "m4", now-100*day, oldText, "[收到]", "/cache/text.gif")},
		{"group:g2", stickerLibraryEvent("bot", "g2", "m5", now-120*day, plain, "[动画表情]", "/cache/plain.gif")},
		{"group:g9", stickerLibraryEvent("other-bot", "g9", "m9", now, foreign, "[别人的]", "/cache/foreign.gif")},
	} {
		if err := store.indexStickerAssets(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []assistant.StickerTagRecord{
		{ContentSHA256: anime, Gist: "少女捂脸", Tags: []string{"害羞", "可爱"}, Category: "二次元", Version: assistant.StickerAnnotationVersion},
		{ContentSHA256: cat, Gist: "猫猫翻白眼", Tags: []string{"无语", "可爱"}, Category: "动物", Version: assistant.StickerAnnotationVersion},
		{ContentSHA256: oldText, Gist: "收到两个字", Tags: []string{"收到"}, Category: "纯文字", Version: assistant.StickerAnnotationVersion},
		{ContentSHA256: foreign, Gist: "别的机器人", Tags: []string{"可爱"}, Category: "二次元", Version: assistant.StickerAnnotationVersion},
	} {
		if err := store.SaveStickerTags(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordStickerSent(ctx, "group:g1", cat, now-day); err != nil {
		t.Fatal(err)
	}

	list := func(query StickerLibraryQuery) []string {
		t.Helper()
		query.ProfileID = "bot"
		page, err := store.ListStickerLibrary(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		hashes := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			hashes = append(hashes, item.Hash[:1])
		}
		if page.Total != len(hashes) {
			t.Fatalf("total=%d items=%v", page.Total, hashes)
		}
		return hashes
	}
	check := func(name string, got []string, want string) {
		t.Helper()
		if strings.Join(got, "") != want {
			t.Fatalf("%s = %v, want %s", name, got, want)
		}
	}
	check("all", list(StickerLibraryQuery{}), "abcd")
	check("二次元", list(StickerLibraryQuery{Category: "二次元"}), "a")
	// 「纯文字」存的时候已经收敛成「文字」。
	check("文字", list(StickerLibraryQuery{Category: "文字"}), "c")
	check("uncategorized", list(StickerLibraryQuery{Category: StickerLibraryUncategorized}), "d")
	check("tag", list(StickerLibraryQuery{Tag: "可爱"}), "ab")
	check("tag+category", list(StickerLibraryQuery{Tag: "可爱", Category: "动物"}), "b")
	check("group g2", list(StickerLibraryQuery{Source: "group:g2"}), "ad")
	check("private", list(StickerLibraryQuery{Source: "private"}), "c")
	check("idle 30d", list(StickerLibraryQuery{IdleDays: 30}), "cd")
	check("never sent", list(StickerLibraryQuery{NeverSent: true}), "acd")
	// b 三天前收的，但机器人昨天发过，和 a 一样算昨天用过。
	check("idle first", list(StickerLibraryQuery{Sort: "idle"}), "dcab")
	check("most sent", list(StickerLibraryQuery{Sort: "most_sent"})[:1], "b")

	page, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", Category: "动物"})
	if err != nil || page.Items[0].Category != "动物" || page.Items[0].SentCount != 1 || page.Items[0].LastSent == nil {
		t.Fatalf("item = %#v err=%v", page.Items, err)
	}

	facets, err := store.ListStickerLibraryFacets(ctx, StickerLibraryQuery{ProfileID: "bot", Source: "group:g1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 画风栏按来源 g1 数，来源栏放开来源、只按机器人数，关键词栏按来源 g1 数。
	if got := facetString(facets.Categories); got != "二次元=1 动物=1" {
		t.Fatalf("categories = %s", got)
	}
	if got := facetString(facets.Sources); got != "group:g1=2 group:g2=2 private=1" {
		t.Fatalf("sources = %s", got)
	}
	if got := facetString(facets.Tags); got != "可爱=2 害羞=1 无语=1" {
		t.Fatalf("tags = %s", got)
	}
}

func facetString(counts []StickerFacetCount) string {
	parts := make([]string, 0, len(counts))
	for _, item := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", item.Value, item.Count))
	}
	return strings.Join(parts, " ")
}

// 按条件清理：先试算不删；按来源清理只移出这个来源的记录，别的群里的同一张还在；
// 清理旧的不拉黑，以后再有人发照样收；勾了拉黑的以后不再收。
func TestCleanupStickerLibrary(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().Unix()
	day := int64(24 * 3600)
	shared := strings.Repeat("a", 64)
	stale := strings.Repeat("b", 64)
	fresh := strings.Repeat("c", 64)
	index := func(session string, event assistant.MessageEvent) {
		t.Helper()
		if err := store.indexStickerAssets(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	index("group:g1", stickerLibraryEvent("bot", "g1", "m1", now-day, shared, "[共享]", "/cache/shared.gif"))
	index("group:g2", stickerLibraryEvent("bot", "g2", "m2", now-day, shared, "[共享]", "/cache/shared.gif"))
	index("group:g1", stickerLibraryEvent("bot", "g1", "m3", now-90*day, stale, "[旧的]", "/cache/stale.gif"))
	index("group:g1", stickerLibraryEvent("bot", "g1", "m4", now, fresh, "[新的]", "/cache/fresh.gif"))

	dry, err := store.CleanupStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", Source: "group:g1"}, false, true)
	if err != nil || dry.Stickers != 3 || dry.Removed != 3 || !dry.DryRun {
		t.Fatalf("dry = %#v err=%v", dry, err)
	}
	if page, _ := store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot"}); page.Total != 3 {
		t.Fatalf("dry run deleted something: %#v", page)
	}

	removed, err := store.CleanupStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", IdleDays: 30}, false, false)
	if err != nil || removed.Stickers != 1 || removed.Removed != 1 {
		t.Fatalf("idle cleanup = %#v err=%v", removed, err)
	}
	// 没拉黑：旧的那张再有人发还能收回来。
	index("group:g1", stickerLibraryEvent("bot", "g1", "m5", now, stale, "[旧的]", "/cache/stale.gif"))

	removed, err = store.CleanupStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", Source: "group:g1"}, true, false)
	if err != nil || removed.Stickers != 3 {
		t.Fatalf("source cleanup = %#v err=%v", removed, err)
	}
	page, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot"})
	if err != nil || page.Total != 1 || page.Items[0].Hash != shared || page.Items[0].GroupID != "g2" {
		t.Fatalf("after source cleanup = %#v err=%v", page, err)
	}
	// 拉黑了：g1 里的那几张以后在哪个群都不再收。
	index("group:g3", stickerLibraryEvent("bot", "g3", "m6", now, fresh, "[新的]", "/cache/fresh.gif"))
	if page, _ := store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot", Source: "group:g3"}); page.Total != 0 {
		t.Fatalf("blocked sticker came back: %#v", page)
	}
}

// 控制台删掉的表情包移出池子，之后再有人发同一张也不收；只拉黑这个机器人的，别的机器人照收。
func TestDeleteStickerAssetBlocksReindex(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sticker-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	weird := strings.Repeat("e", 64)
	for _, item := range []struct {
		session string
		event   assistant.MessageEvent
	}{
		{"group:g1", stickerLibraryEvent("bot", "g1", "m1", 100, weird, "[猎奇]", "/cache/weird.gif")},
		{"group:g2", stickerLibraryEvent("bot", "g2", "m2", 200, weird, "[猎奇]", "/cache/weird.gif")},
		{"group:g9", stickerLibraryEvent("other-bot", "g9", "m9", 300, weird, "[猎奇]", "/cache/weird.gif")},
	} {
		if err := store.indexStickerAssets(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := store.DeleteStickerAsset(ctx, weird, "bot")
	if err != nil || removed != 2 {
		t.Fatalf("removed = %d err=%v", removed, err)
	}
	if err := store.indexStickerAssets(ctx, "group:g3", stickerLibraryEvent("bot", "g3", "m3", 400, weird, "[猎奇]", "/cache/weird.gif")); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "bot"})
	if err != nil || page.Total != 0 {
		t.Fatalf("deleted sticker came back = %#v err=%v", page, err)
	}
	assets, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "group:g3", ProfileID: "bot", ShareGroups: true, SharePrivate: true, Limit: 100})
	if err != nil || len(assets) != 0 {
		t.Fatalf("deleted sticker still a candidate = %#v err=%v", assets, err)
	}
	page, err = store.ListStickerLibrary(ctx, StickerLibraryQuery{ProfileID: "other-bot"})
	if err != nil || page.Total != 1 {
		t.Fatalf("other bot's library = %#v err=%v", page, err)
	}

	// 不选机器人时删除对全部机器人生效。
	if removed, err := store.DeleteStickerAsset(ctx, weird, ""); err != nil || removed != 1 {
		t.Fatalf("global removed = %d err=%v", removed, err)
	}
	if err := store.indexStickerAssets(ctx, "group:g8", stickerLibraryEvent("other-bot", "g8", "m8", 500, weird, "[猎奇]", "/cache/weird.gif")); err != nil {
		t.Fatal(err)
	}
	if page, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{}); err != nil || page.Total != 0 {
		t.Fatalf("globally blocked sticker came back = %#v err=%v", page, err)
	}
}
