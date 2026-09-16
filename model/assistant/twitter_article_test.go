// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fxtwitter 把长文放在 tweet.article：标题、Draft.js 正文块、封面和内联媒体。
const twitterArticleFixture = `{
	"code": 200,
	"tweet": {
		"id": "2100134007984029904",
		"url": "https://x.com/example/status/2100134007984029904",
		"text": "https://x.com/i/article/2099804047448686592",
		"author": {"name": "Example", "screen_name": "example"},
		"article": {
			"id": "2099804047448686592",
			"title": "我为什么要设立这个奖",
			"preview_text": "一\n诺贝尔发明了炸药。",
			"cover_media": {
				"media_id": "2099945992636530688",
				"media_key": "3_2099945992636530688",
				"media_info": {"__typename": "ApiImage", "original_img_url": "https://pbs.twimg.com/media/cover.jpg", "original_img_width": 2500, "original_img_height": 1000}
			},
			"media_entities": [
				{"media_id": "1", "media_info": {"__typename": "ApiImage", "original_img_url": "https://pbs.twimg.com/media/inline.jpg"}},
				{"media_id": "2", "media_info": {"__typename": "ApiVideo", "duration_millis": 12000, "variants": [
					{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/article-low.mp4"},
					{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/article-high.mp4"}
				]}}
			],
			"content": {
				"blocks": [
					{"type": "header-one", "text": "一"},
					{"type": "unstyled", "text": "诺贝尔发明了炸药。"},
					{"type": "unstyled", "text": ""},
					{"type": "atomic", "text": " "},
					{"type": "unordered-list-item", "text": "第一条"},
					{"type": "unordered-list-item", "text": "第二条"},
					{"type": "ordered-list-item", "text": "先做这个"},
					{"type": "ordered-list-item", "text": "再做那个"},
					{"type": "blockquote", "text": "引用一句"},
					{"type": "unstyled", "text": "结尾。"}
				]
			}
		}
	}
}`

func TestParseTwitterPostResponseExpandsAttachedArticle(t *testing.T) {
	post, ok := parseTwitterPostResponse([]byte(twitterArticleFixture))
	if !ok {
		t.Fatal("parseTwitterPostResponse() = false")
	}
	if post.Article.ID != "2099804047448686592" || post.Article.Title != "我为什么要设立这个奖" {
		t.Fatalf("article meta = %#v", post.Article)
	}
	want := strings.Join([]string{
		"一",
		"诺贝尔发明了炸药。",
		"",
		"· 第一条",
		"· 第二条",
		"1. 先做这个",
		"2. 再做那个",
		"「引用一句」",
		"结尾。",
	}, "\n")
	if post.Article.Text != want {
		t.Fatalf("article text = %q, want %q", post.Article.Text, want)
	}
	if len(post.Article.Media) != 3 {
		t.Fatalf("article media = %#v", post.Article.Media)
	}
	if post.Article.Media[0].Type != "photo" || post.Article.Media[0].URL != "https://pbs.twimg.com/media/cover.jpg" {
		t.Fatalf("cover media = %#v", post.Article.Media[0])
	}
	if post.Article.Media[1].URL != "https://pbs.twimg.com/media/inline.jpg" {
		t.Fatalf("inline image = %#v", post.Article.Media[1])
	}
	// 内联视频按码率挑最高的 MP4，和推文里的视频走同一套选择逻辑。
	if post.Article.Media[2].Type != "video" || post.Article.Media[2].downloadURL() != "https://video.twimg.com/article-high.mp4" {
		t.Fatalf("inline video = %#v", post.Article.Media[2])
	}
}

// 转推别人的长文时长文挂在 quote 上，顶层只有一句评论。
func TestParseTwitterPostResponseAdoptsQuotedArticle(t *testing.T) {
	post, ok := parseTwitterPostResponse([]byte(`{
		"tweet": {
			"text": "值得一读",
			"author": {"name": "Diana", "screen_name": "DianaVup"},
			"quote": {
				"text": "https://x.com/i/article/456",
				"article": {"id": "456", "title": "被引用的长文", "content": {"blocks": [{"type": "unstyled", "text": "引用长文的正文"}]}}
			}
		}
	}`))
	if !ok || post.Article.ID != "456" || post.Article.Text != "引用长文的正文" {
		t.Fatalf("post = %#v, ok = %v", post, ok)
	}
}

// 只带长文、没有文案和媒体的响应也必须算有内容，否则会被当成解析失败掉到 yt-dlp。
func TestParseTwitterPostResponseAcceptsArticleOnlyPayload(t *testing.T) {
	post, ok := parseTwitterPostResponse([]byte(`{"tweet":{"article":{"id":"789","title":"只有长文","content":{"blocks":[{"type":"unstyled","text":"正文"}]}}}}`))
	if !ok || post.Article.Title != "只有长文" {
		t.Fatalf("post = %#v, ok = %v", post, ok)
	}
}

func TestTwitterArticleIDRecognizesArticleLinks(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://x.com/i/article/2099804047448686592", "2099804047448686592"},
		{"https://x.com/i/article/2099804047448686592?s=46", "2099804047448686592"},
		{"https://twitter.com/i/articles/123", "123"},
		{"https://x.com/example/article/456", "456"},
		{"https://x.com/example/status/123456", ""},
		{"https://x.com/example/articles", ""},
		{"https://x.com/i/article/not-an-id", ""},
		{"https://example.com/i/article/123", ""},
	}
	for _, test := range tests {
		if got := twitterArticleID(test.raw); got != test.want {
			t.Fatalf("twitterArticleID(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

// 长文链接不能被当成用户主页：/i/article/… 里的 i 不是用户名。
func TestTwitterArticleLinkIsNotAProfileLink(t *testing.T) {
	if handle := twitterProfileHandle("https://x.com/i/article/2099804047448686592"); handle != "" {
		t.Fatalf("twitterProfileHandle() = %q, want empty", handle)
	}
}

func TestTwitterArticleTextTruncatesLongBody(t *testing.T) {
	body := strings.Repeat("长", maximumTwitterArticleRunes+500)
	text := twitterArticleText(twitterArticle{Title: "标题", Text: body})
	if !strings.HasPrefix(text, "长文标题：标题\n长文正文：\n") {
		t.Fatalf("text prefix = %q", text)
	}
	if !strings.Contains(text, "以上是开头部分") {
		t.Fatalf("truncation notice missing: %q", text[len(text)-80:])
	}
	if runes := []rune(text); len(runes) > maximumTwitterArticleRunes+120 {
		t.Fatalf("truncated text is still %d runes", len(runes))
	}
}

func TestTwitterArticleAPIURLCanBeConfigured(t *testing.T) {
	t.Setenv(defaultTwitterArticleAPIEnv, "")
	if got := twitterArticleAPIURL("123"); got != "" {
		t.Fatalf("unconfigured article URL = %q", got)
	}
	t.Setenv(defaultTwitterArticleAPIEnv, "https://resolver.example/article/{id}")
	if got := twitterArticleAPIURL("123"); got != "https://resolver.example/article/123" {
		t.Fatalf("configured id URL = %q", got)
	}
	t.Setenv(defaultTwitterArticleAPIEnv, "https://resolver.example/parse?target={url}")
	got := twitterArticleAPIURL("123")
	if !strings.HasPrefix(got, "https://resolver.example/parse?target=") || !strings.Contains(got, "%2Fi%2Farticle%2F123") {
		t.Fatalf("configured source URL = %q", got)
	}
	t.Setenv(defaultTwitterArticleAPIEnv, "http://resolver.example/article/{id}")
	if got := twitterArticleAPIURL("123"); got != "" {
		t.Fatalf("insecure article URL = %q", got)
	}
	t.Setenv(defaultTwitterArticleAPIEnv, "https://resolver.example/article/{id}")
	if got := twitterArticleAPIURL("../../etc/passwd"); got != "" {
		t.Fatalf("non-numeric article id URL = %q", got)
	}
}

// 推文只放了一个长文链接时，正文必须跟着发出来，封面也当图片发。
func TestResolverTwitterSendsAttachedArticleBody(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_NICKNAME", "小助手")
	resetTwitterArticleIndex()
	t.Cleanup(resetTwitterArticleIndex)
	plugin := NewResolverPlugin(nil)
	plugin.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) {
		return parseTwitterPostResponse([]byte(twitterArticleFixture))
	}
	plugin.twitterMediaDownloader = func(_ context.Context, media twitterMedia) string {
		return map[string]string{
			"https://pbs.twimg.com/media/cover.jpg":    "/tmp/x-cover.jpg",
			"https://pbs.twimg.com/media/inline.jpg":   "/tmp/x-inline.jpg",
			"https://video.twimg.com/article-high.mp4": "/tmp/x-article.mp4",
		}[media.downloadURL()]
	}

	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:  "https://x.com/example/status/2100134007984029904",
		Event: MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if !strings.Contains(resp.Context, "长文标题：我为什么要设立这个奖") || !strings.Contains(resp.Context, "诺贝尔发明了炸药。") {
		t.Fatalf("Context = %q", resp.Context)
	}
	if len(resp.ImageURLs) != 2 || len(resp.VideoURLs) != 1 {
		t.Fatalf("images = %#v, videos = %#v", resp.ImageURLs, resp.VideoURLs)
	}
	if len(resp.ForwardMessages) < 2 || !strings.Contains(resp.ForwardMessages[1].Text, "长文正文：") {
		t.Fatalf("ForwardMessages = %#v", resp.ForwardMessages)
	}
	// 解析过的长文进索引，随后单独发来的长文直链就能直接命中。
	if article, source, ok := rememberedTwitterArticle("2099804047448686592"); !ok ||
		article.Title != "我为什么要设立这个奖" || source != "https://x.com/example/status/2100134007984029904" {
		t.Fatalf("remembered article = %#v, source = %q, ok = %v", article, source, ok)
	}
}

// 同一条消息里推文链接和长文直链一起发时，长文正文由推文那一路展开，
// 长文链接本身不再重复输出整篇，同时把这篇长文记进索引备用。
func TestResolverTwitterArticleLinkDefersToTweetInSameMessage(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_NICKNAME", "小助手")
	resetTwitterArticleIndex()
	t.Cleanup(resetTwitterArticleIndex)
	plugin := NewResolverPlugin(nil)
	var fetched atomic.Int64
	plugin.twitterPostFetcher = func(_ context.Context, raw string) (twitterPost, bool) {
		fetched.Add(1)
		if twitterStatusID(raw) == "" {
			t.Fatalf("长文直链不该按推文去抓：%q", raw)
		}
		return parseTwitterPostResponse([]byte(twitterArticleFixture))
	}
	plugin.twitterMediaDownloader = func(context.Context, twitterMedia) string { return "" }

	req := PluginRequest{
		Text:  "https://x.com/i/article/2099804047448686592 出自 https://x.com/example/status/2100134007984029904",
		Event: MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
	}
	result := plugin.resolveTwitterMedia(context.Background(), req, "https://x.com/i/article/2099804047448686592")
	if !result.Suppressed || strings.TrimSpace(result.Context) != "" {
		t.Fatalf("result = %#v", result)
	}
	if fetched.Load() != 1 {
		t.Fatalf("fetch count = %d", fetched.Load())
	}
	if article, source, ok := rememberedTwitterArticle("2099804047448686592"); !ok ||
		article.Title != "我为什么要设立这个奖" || source != "https://x.com/example/status/2100134007984029904" {
		t.Fatalf("remembered article = %#v, source = %q, ok = %v", article, source, ok)
	}

	// 整条消息走完只有推文那一路输出，正文只出现一次。
	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if got := strings.Count(resp.Context, "长文标题：我为什么要设立这个奖"); got != 1 {
		t.Fatalf("article body appears %d times: %q", got, resp.Context)
	}
}

// 长文直链所在消息里的推文和这篇长文无关时，长文仍然照常发出来。
func TestResolverTwitterArticleLinkKeepsBodyForUnrelatedTweet(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_NICKNAME", "小助手")
	resetTwitterArticleIndex()
	t.Cleanup(resetTwitterArticleIndex)
	post, ok := parseTwitterPostResponse([]byte(twitterArticleFixture))
	if !ok {
		t.Fatal("fixture parse failed")
	}
	rememberTwitterArticle(post.Article, "https://x.com/example/status/2100134007984029904")

	plugin := NewResolverPlugin(nil)
	plugin.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) {
		t.Fatal("索引命中后不应再请求推文接口")
		return twitterPost{}, false
	}
	plugin.twitterMediaDownloader = func(context.Context, twitterMedia) string { return "" }

	result := plugin.resolveTwitterMedia(context.Background(), PluginRequest{
		Text:  "https://x.com/i/article/2099804047448686592 和 https://x.com/other/status/999 一起看",
		Event: MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
	}, "https://x.com/i/article/2099804047448686592")
	if result.Suppressed || !strings.Contains(result.Context, "长文正文：") {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Context, "原推文：https://x.com/example/status/2100134007984029904") {
		t.Fatalf("missing source tweet: %q", result.Context)
	}
	if len(result.ResourceKeys) != 1 || result.ResourceKeys[0] != "x:article:2099804047448686592" {
		t.Fatalf("ResourceKeys = %#v", result.ResourceKeys)
	}
}

// 先解析过推文，随后只发长文直链也要能把正文发出来，且不再请求接口。
func TestResolverTwitterArticleLinkReusesRememberedArticle(t *testing.T) {
	resetTwitterArticleIndex()
	t.Cleanup(resetTwitterArticleIndex)
	post, ok := parseTwitterPostResponse([]byte(twitterArticleFixture))
	if !ok {
		t.Fatal("fixture parse failed")
	}
	rememberTwitterArticle(post.Article, "https://x.com/example/status/2100134007984029904")

	plugin := NewResolverPlugin(nil)
	plugin.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) {
		t.Fatal("索引命中后不应再请求推文接口")
		return twitterPost{}, false
	}
	plugin.twitterMediaDownloader = func(context.Context, twitterMedia) string { return "" }

	result := plugin.resolveTwitterMedia(context.Background(), PluginRequest{
		Text:  "这篇长文讲了什么 https://x.com/i/article/2099804047448686592",
		Event: MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
	}, "https://x.com/i/article/2099804047448686592")
	if !result.Handled || result.Failed || !strings.Contains(result.Context, "长文正文：") {
		t.Fatalf("result = %#v", result)
	}
}

// 既没有可参照的推文也没有自建接口时，必须说清原因和下一步，不能只回「下载失败」。
func TestResolverTwitterArticleLinkExplainsLoginWall(t *testing.T) {
	resetTwitterArticleIndex()
	t.Cleanup(resetTwitterArticleIndex)
	t.Setenv(defaultTwitterArticleAPIEnv, "")
	plugin := NewResolverPlugin(nil)
	plugin.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) { return twitterPost{}, false }
	plugin.twitterMediaDownloader = func(context.Context, twitterMedia) string { return "" }

	result := plugin.resolveTwitterMedia(context.Background(), PluginRequest{
		Text:  "https://x.com/i/article/999",
		Event: MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
	}, "https://x.com/i/article/999")
	if !result.Handled || !result.Failed {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Context, "推文链接") {
		t.Fatalf("Context = %q", result.Context)
	}
	if len(result.VideoURLs) != 0 || len(result.ImageURLs) != 0 {
		t.Fatalf("unexpected media = %#v", result)
	}
}

// 自建长文接口的外层可能是 {"article":…}、{"tweet":{"article":…}} 或长文对象本身，
// 三种都要能解出正文，降级链的第一层才算真的可用。
func TestParseTwitterArticleResponseAcceptsEnvelopeShapes(t *testing.T) {
	body := `{"id":"123","title":"自建接口的长文","content":{"blocks":[{"type":"unstyled","text":"自建接口给的正文"}]}}`
	for name, payload := range map[string]string{
		"bare":    body,
		"article": `{"article":` + body + `}`,
		"tweet":   `{"tweet":{"text":"带长文的推文","article":` + body + `}}`,
		"data":    `{"data":{"article":` + body + `}}`,
	} {
		article, ok := parseTwitterArticleResponse([]byte(payload))
		if !ok || article.Title != "自建接口的长文" || article.Text != "自建接口给的正文" {
			t.Fatalf("%s: article = %#v, ok = %v", name, article, ok)
		}
	}
	if _, ok := parseTwitterArticleResponse([]byte(`{"code":404,"message":"NOT_FOUND","tweet":null}`)); ok {
		t.Fatal("parseTwitterArticleResponse() accepted a not-found payload")
	}
}

func TestTwitterArticleLiveResolvesAttachedArticle(t *testing.T) {
	if os.Getenv("DIANA_TWITTER_LIVE_TEST") != "1" {
		t.Skip("set DIANA_TWITTER_LIVE_TEST=1 to run live X article fetches")
	}
	t.Setenv("DIANA_TWITTER_RESOLVER_API", "")
	resetTwitterArticleIndex()
	t.Cleanup(resetTwitterArticleIndex)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	post, ok := fetchTwitterPost(ctx, "https://x.com/justinsuntron/status/2100134007984029904")
	if !ok {
		t.Fatal("fetchTwitterPost() = false")
	}
	if !post.Article.hasContent() || len([]rune(post.Article.Text)) < 200 {
		t.Fatalf("article = %#v", post.Article)
	}
	t.Logf("article %q: %d runes, %d media", post.Article.Title, len([]rune(post.Article.Text)), len(post.Article.Media))
}
