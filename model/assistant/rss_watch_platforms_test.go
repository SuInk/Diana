// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestRSSWatchPlatformTargetsNormalize(t *testing.T) {
	cases := []struct {
		name             string
		platform, target string
		wantPlatform     string
		wantTarget       string
		wantURL          string
	}{
		{"x 主页链接", rssWatchPlatformX, "https://twitter.com/Tibo", rssWatchPlatformX, "Tibo", "https://x.com/Tibo"},
		{"x @用户名", rssWatchPlatformX, "@tibo", rssWatchPlatformX, "tibo", "https://x.com/tibo"},
		{"自定义 Feed", rssWatchPlatformRSS, "https://example.com/feed.xml#latest", rssWatchPlatformRSS, "https://example.com/feed.xml", "https://example.com/feed.xml"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			source, err := resolveRSSWatchSource(item.platform, item.target, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if source.Platform != item.wantPlatform || source.Target != item.wantTarget || source.URL != item.wantURL {
				t.Fatalf("source=%#v", source)
			}
		})
	}
}

func TestRSSWatchPlatformRejectsWrongTargets(t *testing.T) {
	cases := []struct{ platform, target string }{
		{rssWatchPlatformX, "not a handle"},
		{rssWatchPlatformX, "https://example.com/tibo/extra"},
		{rssWatchPlatformRSS, "ftp://example.com/feed.xml"},
		{rssWatchPlatformRSS, "https://user:pass@example.com/feed.xml"},
		{"bilibili", "2267573"},
	}
	for _, item := range cases {
		if source, err := resolveRSSWatchSource(item.platform, item.target, "", ""); err == nil {
			t.Fatalf("platform=%s target=%s should fail, got %#v", item.platform, item.target, source)
		}
	}
	if _, err := resolveRSSWatchSource("", "", "", ""); err == nil {
		t.Fatal("empty target should fail")
	}
}

func TestRSSWatchSourceKeepsLegacyRecordsWorking(t *testing.T) {
	// 旧字段调用：twitter_handle 等价于 platform=x，feed_url 等价于 platform=rss。
	source, err := resolveRSSWatchSource("", "", "", "@tibo")
	if err != nil || source.Platform != rssWatchPlatformX || source.Target != "tibo" {
		t.Fatalf("source=%#v err=%v", source, err)
	}
	if source, err = resolveRSSWatchSource("", "", "https://example.com/feed.xml", ""); err != nil || source.Platform != rssWatchPlatformRSS {
		t.Fatalf("source=%#v err=%v", source, err)
	}
	// 已保存的旧订阅：FeedSource 是 twitter，仍然按 X 平台抓取。
	legacy := rssWatchSourceFromReminder(Reminder{FeedSource: rssWatchPlatformLegacyTwitter, FeedHandle: "tibo", FeedURL: "https://rsshub.app/twitter/user/tibo"})
	if legacy.Platform != rssWatchPlatformX || legacy.Target != "tibo" {
		t.Fatalf("legacy=%#v", legacy)
	}
	onlyURL := rssWatchSourceFromReminder(Reminder{FeedURL: "https://example.com/feed.xml"})
	if onlyURL.Platform != rssWatchPlatformRSS || onlyURL.Target != "https://example.com/feed.xml" {
		t.Fatalf("onlyURL=%#v", onlyURL)
	}
}

func TestRSSWatchSourceLabels(t *testing.T) {
	cases := []struct {
		source rssWatchSource
		feed   string
		want   string
	}{
		{rssWatchSource{Platform: rssWatchPlatformX, Target: "tibo"}, "", "@tibo"},
		{rssWatchSource{Platform: rssWatchPlatformRSS, URL: "https://example.com/feed.xml"}, "Example", "Example"},
		{rssWatchSource{Platform: rssWatchPlatformRSS, URL: "https://example.com/feed.xml"}, "", "https://example.com/feed.xml"},
	}
	for _, item := range cases {
		if got := rssWatchSourceLabel(item.source, item.feed); got != item.want {
			t.Fatalf("label=%q want %q", got, item.want)
		}
	}
}

func TestApplyFeedTemplateRequiresPlaceholder(t *testing.T) {
	got, err := applyFeedTemplate("https://rss.example.test/twitter/user/{handle}", "tibo")
	if err != nil || got != "https://rss.example.test/twitter/user/tibo" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := applyFeedTemplate("https://rss.example.test/twitter/user", "tibo"); err == nil {
		t.Fatal("template without placeholder should fail")
	}
}

func TestParseXTimelinePage(t *testing.T) {
	page := `<html><head></head><body><script id="__NEXT_DATA__" type="application/json">` +
		`{"props":{"pageProps":{"timeline":{"entries":[` +
		`{"type":"tweet","content":{"tweet":{"id_str":"2","full_text":"额度已经重置","created_at":"2026-08-14T03:00:00.000Z","permalink":"/tibo/status/2","user":{"name":"Tibo","screen_name":"tibo"}}}},` +
		`{"type":"tweet","content":{"tweet":{"id_str":"1","text":"更早的推文","created_at":"2026-08-14T02:00:00.000Z","user":{"name":"Tibo","screen_name":"tibo"}}}}]}}}}` +
		`</script></body></html>`
	feed, err := parseXTimelinePage(page, "tibo")
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "@tibo" || len(feed.Items) != 2 {
		t.Fatalf("feed=%#v", feed)
	}
	newest := feed.Items[0]
	if newest.ID != "tweet:2" || newest.Link != "https://x.com/tibo/status/2" || newest.Content != "额度已经重置" || newest.Author != "Tibo" {
		t.Fatalf("item=%#v", newest)
	}
	if feed.Items[1].Link != "https://x.com/tibo/status/1" {
		t.Fatalf("item=%#v", feed.Items[1])
	}
	// 时间线里同一条推文可能出现在多个位置（引用、转发），去重后不应重复。
	if strings.Count(feed.Items[0].ID+feed.Items[1].ID, "tweet:2") != 1 {
		t.Fatalf("items=%#v", feed.Items)
	}
	if _, err := parseXTimelinePage("Rate limit exceeded", "tibo"); err == nil {
		t.Fatal("rate limited page should fail")
	}
}
