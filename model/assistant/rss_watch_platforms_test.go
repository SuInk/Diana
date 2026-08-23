// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
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
		{"哔哩哔哩空间链接", rssWatchPlatformBilibili, "https://space.bilibili.com/2267573/dynamic", rssWatchPlatformBilibili, "2267573", "https://space.bilibili.com/2267573"},
		{"哔哩哔哩纯 UID", rssWatchPlatformBilibili, "2267573", rssWatchPlatformBilibili, "2267573", "https://space.bilibili.com/2267573"},
		{"抖音主页链接", rssWatchPlatformDouyin, "https://www.douyin.com/user/MS4wLjABAAAARcAHmmF9mAG3JEixq_CdP72APhBlGlLVbN-1eBcPqao", rssWatchPlatformDouyin, "MS4wLjABAAAARcAHmmF9mAG3JEixq_CdP72APhBlGlLVbN-1eBcPqao", "https://www.douyin.com/user/MS4wLjABAAAARcAHmmF9mAG3JEixq_CdP72APhBlGlLVbN-1eBcPqao"},
		{"小红书主页链接", rssWatchPlatformXiaohongshu, "https://www.xiaohongshu.com/user/profile/593032945E87E77791E03696", rssWatchPlatformXiaohongshu, "593032945e87e77791e03696", "https://www.xiaohongshu.com/user/profile/593032945e87e77791e03696"},
		{"GitHub 仓库默认跟 Release", rssWatchPlatformGitHub, "SuInk/Diana", rssWatchPlatformGitHub, "SuInk/Diana/releases", "https://github.com/SuInk/Diana/releases.atom"},
		{"GitHub 分支提交", rssWatchPlatformGitHub, "https://github.com/SuInk/Diana/commits/main", rssWatchPlatformGitHub, "SuInk/Diana/commits/main", "https://github.com/SuInk/Diana/commits/main.atom"},
		{"GitHub 标签", rssWatchPlatformGitHub, "SuInk/Diana/tags", rssWatchPlatformGitHub, "SuInk/Diana/tags", "https://github.com/SuInk/Diana/tags.atom"},
		{"GitHub 用户动态", rssWatchPlatformGitHub, "torvalds", rssWatchPlatformGitHub, "torvalds", "https://github.com/torvalds.atom"},
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
		{rssWatchPlatformBilibili, "https://space.bilibili.com/abc"},
		{rssWatchPlatformBilibili, "https://example.com/2267573"},
		{rssWatchPlatformDouyin, "short"},
		{rssWatchPlatformXiaohongshu, "123"},
		{rssWatchPlatformGitHub, "SuInk/Diana/issues"},
		{rssWatchPlatformRSS, "ftp://example.com/feed.xml"},
		{"tiktok", "someone"},
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
		{rssWatchSource{Platform: rssWatchPlatformBilibili, Target: "2267573"}, "DIYgod 的投稿", "哔哩哔哩 DIYgod 的投稿"},
		{rssWatchSource{Platform: rssWatchPlatformGitHub, Target: "SuInk/Diana/releases"}, "", "GitHub SuInk/Diana/releases"},
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
	got, err := applyFeedTemplate("https://rss.example.test/bilibili/user/{target}", "2267573")
	if err != nil || got != "https://rss.example.test/bilibili/user/2267573" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := applyFeedTemplate("https://rss.example.test/bilibili/user", "2267573"); err == nil {
		t.Fatal("template without placeholder should fail")
	}
}

func TestBilibiliWbiKeyDerivation(t *testing.T) {
	if got := bilibiliKeyFromURL("https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png"); got != "7cd084941338484aae1ad9425b84077c" {
		t.Fatalf("key=%q", got)
	}
	raw := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijkl"
	if got := bilibiliMixinKey(raw); got != "uvscbixgpykfgdtjbrfxhjqtdconmmpn" {
		t.Fatalf("mixin=%q", got)
	}
	if got := bilibiliMixinKey("short"); got != "" {
		t.Fatalf("short key should be empty, got %q", got)
	}
}

func TestParseBilibiliVideos(t *testing.T) {
	body := []byte(`{"code":0,"data":{"list":{"vlist":[
		{"bvid":"BV1hz","title":"新视频","description":"更新说明","author":"DIYgod","created":1766234910,"length":"24:39"},
		{"bvid":"BV1old","title":"旧视频","description":"旧的","author":"DIYgod","created":1766134910,"length":"3:20"}]}}}`)
	feed, err := parseBilibiliVideos(body, "2267573")
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "DIYgod 的投稿" || len(feed.Items) != 2 {
		t.Fatalf("feed=%#v", feed)
	}
	newest := feed.Items[0]
	if newest.ID != "bvid:BV1hz" || newest.Link != "https://www.bilibili.com/video/BV1hz" || !strings.Contains(newest.Content, "时长 24:39") {
		t.Fatalf("item=%#v", newest)
	}
	if !newest.PublishedAt.Equal(time.Unix(1766234910, 0).UTC()) {
		t.Fatalf("published=%s", newest.PublishedAt)
	}
	if _, err := parseBilibiliVideos([]byte(`{"code":-352,"message":"-352"}`), "2267573"); err == nil || !strings.Contains(err.Error(), "风控") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseBilibiliDynamics(t *testing.T) {
	body := []byte(`{"code":0,"data":{"items":[
		{"id_str":"901","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"name":"DIYgod","pub_ts":1766234910},
			"module_dynamic":{"desc":{"text":"发了个视频"},"major":{"type":"MAJOR_TYPE_ARCHIVE","archive":{"bvid":"BV1hz","title":"新视频","desc":"简介"}}}}},
		{"id_str":"900","type":"DYNAMIC_TYPE_FORWARD","modules":{"module_author":{"name":"DIYgod","pub_ts":1766134910},
			"module_dynamic":{"desc":{"text":"转个"},"major":{}}},
			"orig":{"id_str":"899","modules":{"module_author":{"name":"别人"},"module_dynamic":{"desc":{"text":"原动态"},"major":{}}}}}]}}`)
	feed, err := parseBilibiliDynamics(body, "2267573")
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "DIYgod 的动态" || len(feed.Items) != 2 {
		t.Fatalf("feed=%#v", feed)
	}
	if feed.Items[0].Link != "https://www.bilibili.com/video/BV1hz" || feed.Items[0].Title != "新视频" {
		t.Fatalf("item=%#v", feed.Items[0])
	}
	forwarded := feed.Items[1]
	if forwarded.Link != "https://t.bilibili.com/900" || !strings.Contains(forwarded.Content, "转发自 别人：原动态") {
		t.Fatalf("item=%#v", forwarded)
	}
}

func TestParseDouyinPosts(t *testing.T) {
	body := []byte(`{"status_code":0,"aweme_list":[
		{"aweme_id":"7500","desc":"第一行\n第二行","create_time":1766234910,"author":{"nickname":"某个作者"},
		 "statistics":{"digg_count":12,"comment_count":3},"video_tag":[{"tag_name":"生活"}]}]}`)
	feed, err := parseDouyinPosts(body, "MS4wLjABAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "某个作者 的抖音作品" || len(feed.Items) != 1 {
		t.Fatalf("feed=%#v", feed)
	}
	item := feed.Items[0]
	if item.Title != "第一行" || item.Link != "https://www.douyin.com/video/7500" || !strings.Contains(item.Content, "生活") || !strings.Contains(item.Content, "点赞 12") {
		t.Fatalf("item=%#v", item)
	}
	if _, err := parseDouyinPosts([]byte(`{"status_code":8,"status_msg":"风控","aweme_list":[]}`), "MS4wLjABAAAA"); err == nil {
		t.Fatal("blocked response should fail")
	}
}

func TestParseXiaohongshuProfile(t *testing.T) {
	page := `<html><body><script>window.__INITIAL_STATE__={"user":{"userPageData":{"_rawValue":{"basicInfo":{"nickname":"小宇菇菇","desc":undefined}}},` +
		`"notes":{"_rawValue":[[{"noteCard":{"type":"video","displayTitle":"新笔记 undefined 也算正文","noteId":"abc123","time":1746701673000,` +
		`"xsecToken":"TOKEN","user":{"nickName":"小宇菇菇"},"interactInfo":{"likedCount":"10万+"}}}],` +
		`[{"noteCard":{"type":"normal","displayTitle":"旧笔记","noteId":"","time":1746601673000,"user":{"nickname":"小宇菇菇"},"interactInfo":{"likedCount":"3"}}}]]}}}</script></body></html>`
	feed, err := parseXiaohongshuProfile(page, "593032945e87e77791e03696")
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "小宇菇菇 的小红书笔记" || len(feed.Items) != 2 {
		t.Fatalf("feed=%#v", feed)
	}
	newest := feed.Items[0]
	if newest.ID != "note:abc123" || !strings.HasPrefix(newest.Link, "https://www.xiaohongshu.com/explore/abc123?xsec_token=TOKEN") {
		t.Fatalf("item=%#v", newest)
	}
	if !strings.Contains(newest.Title, "undefined 也算正文") {
		t.Fatalf("title=%q", newest.Title)
	}
	if !newest.PublishedAt.Equal(time.UnixMilli(1746701673000).UTC()) {
		t.Fatalf("published=%s", newest.PublishedAt)
	}
	// 没有笔记 ID 时退回主页链接，条目 ID 由内容摘要保证稳定。
	oldest := feed.Items[1]
	if oldest.Link != "https://www.xiaohongshu.com/user/profile/593032945e87e77791e03696" || !strings.HasPrefix(oldest.ID, "sha256:") {
		t.Fatalf("item=%#v", oldest)
	}
	if _, err := parseXiaohongshuProfile("<html><body>验证码</body></html>", "593032945e87e77791e03696"); err == nil {
		t.Fatal("page without state should fail")
	}
}

func TestReplaceJSONUndefinedKeepsStrings(t *testing.T) {
	got := replaceJSONUndefined(`{"a":undefined,"b":"undefined 出现在正文里","c":[undefined,undefined],"d":"引号\"undefined\""}`)
	want := `{"a":null,"b":"undefined 出现在正文里","c":[null,null],"d":"引号\"undefined\""}`
	if got != want {
		t.Fatalf("got=%s", got)
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
	if _, err := parseXTimelinePage("Rate limit exceeded", "tibo"); err == nil {
		t.Fatal("rate limited page should fail")
	}
}
