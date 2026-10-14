// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/url"
	"strings"
	"testing"
)

func TestWeiboStatusID(t *testing.T) {
	cases := map[string]string{
		"https://m.weibo.cn/detail/5234367615996775":                           "5234367615996775",
		"https://m.weibo.cn/status/ABcd1234E":                                  "ABcd1234E",
		"https://m.weibo.cn/7777777/ABcd1234E":                                 "ABcd1234E",
		"https://weibo.com/7777777/P5kWdcfDe":                                  "P5kWdcfDe",
		"https://weibo.com/tv/show/1034:5007449447661594?mid=5112721292920028": "P5kWdcfDe",
		"https://video.weibo.com/show?fid=1034:5145615399845897":               "",
		"https://weibo.com/ttarticle/p/show?id=2309404962180771742222":         "",
		"https://weibo.com/u/7777777":                                          "",
		"https://example.com/7777777/abc":                                      "",
	}
	for raw, want := range cases {
		if got := weiboStatusID(raw); got != want {
			t.Errorf("weiboStatusID(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWeiboMidToID(t *testing.T) {
	if got := weiboMidToID("5112721292920028"); got != "P5kWdcfDe" {
		t.Fatalf("weiboMidToID = %q", got)
	}
}

func TestParseWeiboResponse(t *testing.T) {
	if _, reason := parseWeiboResponse([]byte(`{"ok":0,"errno":"20170","msg":"hidden"}`)); reason != "invisible" {
		t.Fatalf("reason = %q", reason)
	}
	if _, reason := parseWeiboResponse([]byte(`<html>Sina Visitor System</html>`)); reason != "error" {
		t.Fatalf("reason = %q", reason)
	}
	body := `{"ok":1,"data":{"id":"1","text":"今天天气好<br />出去玩 <span class=\"url-icon\"><img alt=\"[哈哈]\" src=\"x.png\"></span> <a href=\"/n/a\">@某人</a>&amp;朋友",
		"user":{"screen_name":"测试博主"},
		"pics":[{"url":"https://img.example.com/m/1.jpg","large":{"url":"https://img.example.com/l/1.jpg"}},{"url":"https://img.example.com/m/2.jpg","large":{"url":""}}],
		"page_info":{"title":"一段视频","page_pic":{"url":"https://img.example.com/cover.jpg"},"urls":{"mp4_hd_mp4":"https://video.example.com/hd.mp4","mp4_ld_mp4":"https://video.example.com/ld.mp4"}},
		"retweeted_status":{"text":"原微博","user":{"screen_name":"原作者"}}}}`
	status, reason := parseWeiboResponse([]byte(body))
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	if got := status.imageURLs(); len(got) != 2 || got[0] != "https://img.example.com/l/1.jpg" || got[1] != "https://img.example.com/m/2.jpg" {
		t.Fatalf("images = %#v", got)
	}
	if got := status.videoURL(); got != "https://video.example.com/hd.mp4" {
		t.Fatalf("video = %q", got)
	}
	text := weiboContext(status)
	for _, want := range []string{"[微博] 测试博主", "今天天气好\n出去玩 [哈哈] @某人&朋友", "视频：一段视频", "转发自 @原作者：原微博"} {
		if !strings.Contains(text, want) {
			t.Errorf("context missing %q:\n%s", want, text)
		}
	}
}

func TestHeyboxLinkID(t *testing.T) {
	cases := map[string]string{
		"https://www.xiaoheihe.cn/app/bbs/link/61c8729f7180?h_camp=link&h_src=YXBwX3NoYXJl": "61c8729f7180",
		"https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=abc123&foo=1":            "abc123",
		"https://www.xiaoheihe.cn/app/topic/game/pc/12345":                                  "",
		"https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=a/../b":                  "",
		"https://example.com/app/bbs/link/abc":                                              "",
	}
	for raw, want := range cases {
		if got := heyboxLinkID(raw); got != want {
			t.Errorf("heyboxLinkID(%q) = %q, want %q", raw, got, want)
		}
	}
}

// 期望值由参考实现 nonebot-plugin-parser 的 get_hkey 在相同输入下算出（它内部会把时间戳加 1）。
func TestHeyboxHkeyMatchesReference(t *testing.T) {
	if got := heyboxHkey(heyboxLinkTreePath, 1700000001, "0123456789ABCDEF0123456789ABCDEF"); got != "YT27P47" {
		t.Fatalf("hkey = %q", got)
	}
	if got := heyboxHkey(heyboxLinkTreePath, 1791871825, "ABCDEF0123456789ABCDEF0123456789"); got != "UW3PX14" {
		t.Fatalf("hkey = %q", got)
	}
}

func TestHeyboxLinkTreeURL(t *testing.T) {
	raw := heyboxLinkTreeURL("abc123", 1700000000, "0123456789ABCDEF0123456789ABCDEF")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Host != "api.xiaoheihe.cn" || parsed.Path != heyboxLinkTreePath ||
		query.Get("hkey") != "YT27P47" || query.Get("link_id") != "abc123" || query.Get("x_client_type") != "weboutapp" {
		t.Fatalf("url = %s", raw)
	}
	if query.Has("device_id") || query.Has("x_xhh_tokenid") {
		t.Fatalf("url should not carry device identity: %s", raw)
	}
}

func TestParseHeyboxResponse(t *testing.T) {
	if _, reason := parseHeyboxResponse([]byte(`{"msg":"","result":{},"status":"show_captcha"}`)); reason != "captcha" {
		t.Fatalf("reason = %q", reason)
	}
	if _, reason := parseHeyboxResponse([]byte(`{"status":"failed","msg":"非法请求"}`)); reason != "error" {
		t.Fatalf("reason = %q", reason)
	}
	body := `{"status":"ok","result":{"link":{"title":"测试帖子","description":"摘要","has_video":1,"video_url":"https://video.example.com/v.mp4","video_thumb":"https://img.example.com/t.jpg",
		"click":10,"link_award_num":5,"comment_num":3,"user":{"username":"测试用户"},
		"text":"[{\"type\":\"html\",\"text\":\"<p></p><p><img class=\\\"lazy\\\" data-original=\\\"https://img.example.com/a.png\\\"/></p><p>第一段&nbsp;内容</p><p>第二段</p><p><img data-original=\\\"https://img.example.com/b.png?x=1&amp;y=2\\\"/></p>\"},{\"type\":\"img\",\"url\":\"https://img.example.com/dup.png\"}]"}}}`
	link, reason := parseHeyboxResponse([]byte(body))
	if reason != "" {
		t.Fatalf("reason = %q", reason)
	}
	text, images := heyboxBody(link.Text)
	if text != "第一段 内容\n第二段" {
		t.Fatalf("text = %q", text)
	}
	if len(images) != 2 || images[0] != "https://img.example.com/a.png" || images[1] != `https://img.example.com/b.png?x=1&y=2\` {
		t.Fatalf("images = %#v", images)
	}
	ctx := heyboxContext(link, text)
	for _, want := range []string{"[小黑盒] 测试帖子", "作者：测试用户", "浏览 10 · 赞 5 · 评论 3", "第一段 内容"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context missing %q:\n%s", want, ctx)
		}
	}
}

func TestHeyboxBodyImageSegments(t *testing.T) {
	text, images := heyboxBody(`[{"type":"text","text":"前言"},{"type":"img","url":"https://img.example.com/1.png"}]`)
	if text != "前言" || len(images) != 1 || images[0] != "https://img.example.com/1.png" {
		t.Fatalf("text=%q images=%#v", text, images)
	}
}

func TestWeiboAndHeyboxResourceKeys(t *testing.T) {
	if got := resolverResourceKeyFromURL("weibo", "https://weibo.com/7777777/P5kWdcfDe"); got != "weibo:P5kWdcfDe" {
		t.Fatalf("weibo key = %q", got)
	}
	if got := resolverResourceKeyFromURL("heybox", "https://www.xiaoheihe.cn/app/bbs/link/abc123"); got != "heybox:abc123" {
		t.Fatalf("heybox key = %q", got)
	}
}
