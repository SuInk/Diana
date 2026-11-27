// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolverCountFormatsChineseUnits(t *testing.T) {
	cases := map[int64]string{
		0:           "",
		-1:          "",
		9999:        "9999",
		10000:       "1万",
		123456:      "12.3万",
		99999999:    "9999.9万",
		100000000:   "1亿",
		12345678901: "123.4亿",
	}
	for value, want := range cases {
		if got := string(resolverCountFromInt(value)); got != want {
			t.Errorf("resolverCountFromInt(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestResolverCountDecodesLenientJSON(t *testing.T) {
	var payload struct {
		A, B, C, D, E, F resolverCount
	}
	body := `{"A":123456,"B":"1.2万","C":"100万+","D":null,"E":[1,2],"F":{"count":3}}`
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := []resolverCount{payload.A, payload.B, payload.C, payload.D, payload.E, payload.F}
	want := []resolverCount{"12.3万", "1.2万", "100万+", "", "", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolverStatsLineSkipsMissingCounts(t *testing.T) {
	got := resolverStatsLine(
		resolverStat{"播放", resolverCountFromInt(123456)},
		resolverStat{"点赞", ""},
		resolverStat{"收藏", "0"},
		resolverStat{"评论", "12"},
	)
	if got != "播放 12.3万 · 评论 12" {
		t.Fatalf("stats line = %q", got)
	}
	if got := appendResolverStatsLine("标题", resolverStat{"点赞", ""}); got != "标题" {
		t.Fatalf("empty stats changed text: %q", got)
	}
}

func TestBilibiliStatsIncludePlayLikeCoinFavorite(t *testing.T) {
	var view bilibiliViewResponse
	view.Data.Stat.View = 1234567
	view.Data.Stat.Like = 8901
	view.Data.Stat.Coin = 234
	view.Data.Stat.Favorite = 56789
	if got := resolverStatsLine(bilibiliStats(view)...); got != "播放 123.4万 · 点赞 8901 · 投币 234 · 收藏 5.6万" {
		t.Fatalf("bilibili stats = %q", got)
	}
}

func TestDouyinDetailDecodesStatistics(t *testing.T) {
	var detail douyinMediaDetail
	body := `{"aweme_id":"1","statistics":{"play_count":0,"digg_count":23456,"collect_count":789,"comment_count":10,"share_count":5}}`
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resolverStatsLine(detail.stats()...); got != "点赞 2.3万 · 收藏 789 · 评论 10 · 分享 5" {
		t.Fatalf("douyin stats = %q", got)
	}
}

func TestXiaohongshuSocialContextIncludesInteractCounts(t *testing.T) {
	text := xiaohongshuSocialContext(map[string]any{
		"title":        "测试标题",
		"desc":         "正文",
		"user":         map[string]any{"nickname": "测试作者"},
		"interactInfo": map[string]any{"likedCount": "1.2万", "collectedCount": "3456", "commentCount": float64(78), "shareCount": "0"},
	})
	want := "[小红书] 测试标题\n作者：测试作者\n正文\n点赞 1.2万 · 收藏 3456 · 评论 78"
	if text != want {
		t.Fatalf("context = %q, want %q", text, want)
	}
}

func TestWeiboContextEndsWithCounts(t *testing.T) {
	status, reason := parseWeiboResponse([]byte(`{"ok":1,"data":{"id":"1","text":"正文","user":{"screen_name":"测试博主"},"attitudes_count":"100万+","reposts_count":2345,"comments_count":67,"retweeted_status":{"text":"原微博","user":{"screen_name":"原作者"}}}}`))
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if text := weiboContext(status); !strings.HasSuffix(text, "转发自 @原作者：原微博\n点赞 100万+ · 转发 2345 · 评论 67") {
		t.Fatalf("stats should follow the retweet:\n%s", text)
	}
}

func TestTwitterStatsStayOnFirstPost(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_NICKNAME", "测试")
	post, ok := parseTwitterPostResponse([]byte(`{"tweet":{"id":"1","text":"hello","author":{"name":"测试","screen_name":"tester"},"views":1234567,"likes":8901,"retweets":234,"bookmarks":56,"replies":7}}`))
	if !ok {
		t.Fatal("parse failed")
	}
	post.Replies = []twitterPost{{ID: "2", Text: "后续"}}
	const stats = "浏览 123.4万 · 点赞 8901 · 转推 234 · 收藏 56 · 回复 7"
	plugin := NewResolverPlugin(nil)
	plugin.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) { return post, true }
	result := plugin.resolveTwitterMedia(context.Background(), PluginRequest{}, "https://x.com/tester/status/1")
	if len(result.ForwardMessages) != 2 {
		t.Fatalf("nodes = %#v", result.ForwardMessages)
	}
	if !strings.HasSuffix(result.ForwardMessages[0].Text, "文案：hello\n"+stats) {
		t.Fatalf("stats should follow the first post: %q", result.ForwardMessages[0].Text)
	}
	if strings.Contains(result.ForwardMessages[1].Text, stats) {
		t.Fatalf("follow-up should not carry stats: %q", result.ForwardMessages[1].Text)
	}
	if !strings.Contains(result.Context, stats+"\n\n作者补充 1：后续") || strings.HasSuffix(result.Context, stats) {
		t.Fatalf("context stats should sit before follow-ups:\n%s", result.Context)
	}
}
