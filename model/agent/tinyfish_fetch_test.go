// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 公网 IP 字面量：地址校验不用查 DNS，测试离线也能跑。
const tinyFishTestPage = "https://1.1.1.1/docs/intro"

func resetTinyFishCache(t *testing.T) {
	t.Helper()
	previous := tinyFishPages
	tinyFishPages = &tinyFishPageCache{entries: map[string]tinyFishCachedPage{}}
	t.Cleanup(func() { tinyFishPages = previous })
}

// 读网页照参考 bot 的请求形状发：单个网址、markdown，另外要链接和图片列表；整页存进
// FullText 给 find 用，交给模型的正文按上限截断。同一网址 15 分钟内不再重抓。
func TestTinyFishFetchReadsMarkdownAndCachesByURL(t *testing.T) {
	resetTinyFishCache(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("request = %s key=%q", r.Method, r.Header.Get("X-API-Key"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		urls, _ := body["urls"].([]any)
		if len(urls) != 1 || urls[0] != tinyFishTestPage || body["format"] != "markdown" || body["links"] != true || body["image_links"] != true {
			t.Errorf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"url": tinyFishTestPage, "final_url": "https://1.1.1.1/docs/v2/intro", "title": "  Intro\n Page ", "description": "简介", "text": "# 标题\n" + strings.Repeat("正文", 100), "links": []string{"https://1.1.1.1/docs/v2/intro#top", "https://1.1.1.1/docs/v2/intro", "https://1.1.1.1/docs/next", "javascript:void(0)", "/relative"}}}})
	}))
	defer server.Close()

	page, err := fetchTinyFishPage(context.Background(), "test-key", server.URL, tinyFishTestPage, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Intro Page" || page.Description != "简介" || page.SourceType != "tinyfish_fetch" || page.RequestedURL != tinyFishTestPage || page.URL != "https://1.1.1.1/docs/v2/intro" || !page.Stable {
		t.Fatalf("page = %#v", page)
	}
	// 链接去掉锚点、去重，只留绝对 http(s) 地址。
	if len(page.Links) != 2 || page.Links[0].URL != "https://1.1.1.1/docs/v2/intro" || page.Links[1].URL != "https://1.1.1.1/docs/next" {
		t.Fatalf("links = %#v", page.Links)
	}
	if !page.Truncated || !strings.HasPrefix(page.Text, string([]rune(page.FullText)[:50])) || strings.Contains(page.Text, string([]rune(page.FullText)[:51])) || !strings.HasSuffix(page.FullText, "正文正文") {
		t.Fatalf("text=%q truncated=%v full=%d", page.Text, page.Truncated, len([]rune(page.FullText)))
	}

	again, err := fetchTinyFishPage(context.Background(), "test-key", server.URL, tinyFishTestPage, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("同一网址重抓了 %d 次", calls.Load())
	}
	if again.Truncated || again.Text != again.FullText {
		t.Fatalf("缓存命中后没按这次的上限截断：%#v", again.Truncated)
	}
}

// fetch 接口报错（被拦、空正文、HTTP 错误）要包成 errTinyFishFetch，调用方据此退回
// 本地浏览器；内网地址照旧在发请求前拒掉。
func TestTinyFishFetchFailuresAreRecognizable(t *testing.T) {
	resetTinyFishCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blocked":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "errors": []map[string]any{{"error": "bot_blocked"}}})
		case "/empty":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"text": "  "}}})
		default:
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer server.Close()

	for path, want := range map[string]string{"/blocked": "bot_blocked", "/empty": "empty_content", "/limited": "HTTP 429"} {
		_, err := fetchTinyFishPage(context.Background(), "test-key", server.URL+path, tinyFishTestPage, 100)
		if !errors.Is(err, errTinyFishFetch) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v", path, err)
		}
	}
	if _, err := fetchTinyFishPage(context.Background(), "test-key", server.URL, "http://127.0.0.1/admin", 100); err == nil || errors.Is(err, errTinyFishFetch) {
		t.Fatalf("内网地址应在请求前被拒：%v", err)
	}
	if _, err := fetchTinyFishPage(context.Background(), "", server.URL, tinyFishTestPage, 100); !errors.Is(err, errTinyFishFetch) {
		t.Fatalf("没有 key 时 err = %v", err)
	}
}

// 配了 key 时浏览器渲染器先走 fetch，成功就不起 Chrome；这里给一个不存在的浏览器路径，
// 走到浏览器就会报找不到可执行文件。
func TestSandboxedBrowserPrefersTinyFishFetch(t *testing.T) {
	resetTinyFishCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"url": tinyFishTestPage, "title": "Docs", "text": "hello"}}})
	}))
	defer server.Close()
	browser := NewSandboxedBrowser(SandboxedBrowserConfig{Executable: "/nonexistent/chrome", FetchAPIKey: "test-key", FetchEndpoint: server.URL})
	page, err := browser.Render(context.Background(), tinyFishTestPage)
	if err != nil || page.Text != "hello" || page.BrowserEngine != "tinyfish" {
		t.Fatalf("page=%#v err=%v", page, err)
	}

	// fetch 失败就退回浏览器；浏览器也起不来时两个原因都交回去。
	resetTinyFishCache(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer failing.Close()
	browser = NewSandboxedBrowser(SandboxedBrowserConfig{Executable: "/nonexistent/chrome", FetchAPIKey: "test-key", FetchEndpoint: failing.URL})
	_, err = browser.Render(context.Background(), tinyFishTestPage)
	if err == nil || !errors.Is(err, errTinyFishFetch) || !strings.Contains(err.Error(), "/nonexistent/chrome") {
		t.Fatalf("fallback err = %v", err)
	}
}

// fetch 带回的图片直链要进 page.Images 并交给模型：11-09 照 X 主页头像画图时，没有直链
// 模型只能截整页当底图。同一张 twimg 头像的各尺寸合成一条、换成最大的，留在原位置；
// 推文配图按 name 参数合并；SVG、非 http(s) 和重复地址跳过。
func TestTinyFishFetchReturnsImageLinks(t *testing.T) {
	resetTinyFishCache(t)
	const avatar = "https://pbs.twimg.com/profile_images/10001/AbCd_400x400.jpg"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
			"url":  tinyFishTestPage,
			"text": "主页",
			"image_links": []string{
				"https://pbs.twimg.com/profile_images/10001/AbCd_200x200.jpg",
				"https://pbs.twimg.com/profile_images/10001/AbCd_mini.jpg",
				"https://pbs.twimg.com/profile_banners/10001/1700000000/1500x500",
				avatar,
				"https://pbs.twimg.com/profile_images/10001/AbCd_normal.jpg",
				"https://pbs.twimg.com/profile_images/10001/AbCd_x96.jpg",
				"https://pbs.twimg.com/media/Xyz?format=jpg&name=small",
				"https://pbs.twimg.com/media/Xyz?format=jpg&name=large",
				"https://abs.twimg.com/icons/logo.svg",
				"data:image/png;base64,AAAA",
				"https://1.1.1.1/a.png#x",
				"https://1.1.1.1/a.png",
			},
		}}})
	}))
	defer server.Close()

	page, err := fetchTinyFishPage(context.Background(), "test-key", server.URL, tinyFishTestPage, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{avatar, "https://pbs.twimg.com/profile_banners/10001/1700000000/1500x500", "https://pbs.twimg.com/media/Xyz?format=jpg&name=large", "https://1.1.1.1/a.png"}
	if len(page.Images) != len(want) {
		t.Fatalf("images = %#v", page.Images)
	}
	for i, image := range page.Images {
		if image.URL != want[i] {
			t.Fatalf("images[%d] = %q, want %q", i, image.URL, want[i])
		}
	}

	// 缓存命中也带着图片，browser_render 的输出里能看到直链。
	cached, err := fetchTinyFishPage(context.Background(), "test-key", server.URL, tinyFishTestPage, 10)
	if err != nil || len(cached.Images) != len(want) {
		t.Fatalf("cached images = %#v err=%v", cached.Images, err)
	}
	output, err := browserRenderOutput(cached, "")
	if err != nil || !strings.Contains(output, `"images"`) || !strings.Contains(output, avatar) {
		t.Fatalf("output = %s err=%v", output, err)
	}
}

// 图片列表最多留 maxRenderedPageImages 张，超出的新图不再挤进来，但已占位的图仍能换成更大的尺寸。
func TestTinyFishImagesCapKeepsUpgradingKnownVariants(t *testing.T) {
	var raw []string
	raw = append(raw, "https://pbs.twimg.com/profile_images/1/A_normal.png")
	for i := 0; i < maxRenderedPageImages+3; i++ {
		raw = append(raw, "https://1.1.1.1/img/"+strings.Repeat("x", i+1)+".jpg")
	}
	raw = append(raw, "https://pbs.twimg.com/profile_images/1/A.png")
	images := tinyFishImages(raw)
	if len(images) != maxRenderedPageImages || images[0].URL != "https://pbs.twimg.com/profile_images/1/A.png" {
		t.Fatalf("images = %#v", images)
	}
}

// engine=browser 跳过 fetch 和它的缓存，直接用本地浏览器；浏览器起不来就明确报错，
// 不悄悄退回快速读取。单页和批量都一样。不填 engine 时照旧先走 fetch。
func TestBrowserRenderEngineBrowserSkipsTinyFish(t *testing.T) {
	resetTinyFishCache(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"url": tinyFishTestPage, "title": "Docs", "text": "hello"}}})
	}))
	defer server.Close()
	tool := NewBrowserRenderTool(NewSandboxedBrowser(SandboxedBrowserConfig{Executable: "/nonexistent/chrome", FetchAPIKey: "test-key", FetchEndpoint: server.URL}))

	output, err := tool.Run(context.Background(), map[string]any{"url": tinyFishTestPage})
	if err != nil || !strings.Contains(output, "hello") || calls.Load() != 1 {
		t.Fatalf("默认应走 fetch：output=%s err=%v calls=%d", output, err, calls.Load())
	}

	// 缓存里已经有这一页，指定 browser 也不能拿缓存交差。
	_, err = tool.Run(context.Background(), map[string]any{"url": tinyFishTestPage, "engine": "browser"})
	if err == nil || !strings.Contains(err.Error(), "engine=browser") || !strings.Contains(err.Error(), "/nonexistent/chrome") || errors.Is(err, errTinyFishFetch) {
		t.Fatalf("engine=browser err = %v", err)
	}
	_, err = tool.Run(context.Background(), map[string]any{"url": tinyFishTestPage, "urls": []any{"https://1.1.1.1/docs/next"}, "engine": "browser"})
	if err == nil || !strings.Contains(err.Error(), "engine=browser") {
		t.Fatalf("批量 engine=browser err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("engine=browser 仍请求了 fetch：%d 次", calls.Load())
	}

	schema := tool.InputSchema()
	engine, _ := schema["properties"].(map[string]any)["engine"].(map[string]any)
	if values, _ := engine["enum"].([]string); len(values) != 2 || values[0] != RenderEngineAuto || values[1] != RenderEngineBrowser {
		t.Fatalf("engine schema = %#v", engine)
	}
}
