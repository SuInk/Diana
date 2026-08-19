// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// TestResolverFetchesOGMetaAfterRedirect 验证对应功能场景。
func TestResolverFetchesOGMetaAfterRedirect(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/short":
			http.Redirect(w, r, "/note/1", http.StatusFound)
		case "/note/1":
			hits.Add(1)
			if !strings.Contains(r.Header.Get("Accept-Language"), "zh-CN") {
				t.Errorf("missing zh Accept-Language, got %q", r.Header.Get("Accept-Language"))
			}
			if !strings.Contains(r.Header.Get("User-Agent"), "Chrome") {
				t.Errorf("missing browser UA, got %q", r.Header.Get("User-Agent"))
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head>
				<title>通用标题 - 某站</title>
				<meta content="真实笔记标题" property="og:title">
				<meta property="og:description" content="这是笔记摘要&amp;说明">
				</head><body>JS 渲染正文</body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	link := server.URL + "/short"
	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "看这个 " + link})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	// og:title 优先于 <title>，属性顺序颠倒也要能解析；实体要还原。
	if !strings.Contains(resp.Context, "标题：真实笔记标题") {
		t.Fatalf("Context missing og title: %q", resp.Context)
	}
	if !strings.Contains(resp.Context, "摘要：这是笔记摘要&说明") {
		t.Fatalf("Context missing description: %q", resp.Context)
	}

	// 第二次解析同一链接应命中缓存，不再打到源站。
	before := hits.Load()
	if _, err := plugin.Handle(context.Background(), PluginRequest{Text: link}); err != nil {
		t.Fatalf("Handle()#2 error = %v", err)
	}
	if hits.Load() != before {
		t.Fatalf("cache miss: hits %d -> %d", before, hits.Load())
	}
}

// TestResolverMarksBlockedPages 验证对应功能场景。
func TestResolverMarksBlockedPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: server.URL})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Context, "未能获取网页内容") {
		t.Fatalf("Context = %#v", resp)
	}
}

// TestResolverBrowserFallbackWhenBlocked 验证对应功能场景。
func TestResolverBrowserFallbackWhenBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	var gotCDPURL string
	plugin.browserFetch = func(_ context.Context, cdpURL string, pageURL string) (agent.RenderedPage, error) {
		gotCDPURL = cdpURL
		return agent.RenderedPage{
			URL:         pageURL,
			Title:       "渲染标题",
			Description: "渲染摘要",
		}, nil
	}

	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text: server.URL,
		Settings: SettingValues{
			"browser_render":  true,
			"browser_cdp_url": "http://127.0.0.1:19222",
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if gotCDPURL != "http://127.0.0.1:19222" {
		t.Fatalf("cdp url = %q", gotCDPURL)
	}
	if !strings.Contains(resp.Context, "标题：渲染标题") || !strings.Contains(resp.Context, "摘要：渲染摘要") {
		t.Fatalf("Context = %q", resp.Context)
	}
	// 浏览器兜底成功后不应再有抓取失败备注。
	if strings.Contains(resp.Context, "未能获取网页内容") {
		t.Fatalf("Context still marks failure: %q", resp.Context)
	}
}

// TestResolverBrowserFallbackRejectsBlockedPageText 验证对应功能场景。
func TestResolverBrowserFallbackRejectsBlockedPageText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	plugin.browserFetch = func(_ context.Context, _ string, pageURL string) (agent.RenderedPage, error) {
		// 模拟风控页：没有标题，正文只有限制访问提示。
		return agent.RenderedPage{URL: pageURL, Text: `{"error":{"message":"您当前请求存在异常，暂时限制本次访问。"}}`}, nil
	}

	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:     server.URL,
		Settings: SettingValues{"browser_render": true},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if strings.Contains(resp.Context, "摘要：") {
		t.Fatalf("blocked text leaked into summary: %q", resp.Context)
	}
	if !strings.Contains(resp.Context, "未能获取网页内容") {
		t.Fatalf("Context = %q", resp.Context)
	}
}

// TestResolverBrowserFallbackDisabledByDefault 验证对应功能场景。
func TestResolverBrowserFallbackDisabledByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	called := false
	plugin.browserFetch = func(context.Context, string, string) (agent.RenderedPage, error) {
		called = true
		return agent.RenderedPage{}, nil
	}

	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: server.URL})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if called {
		t.Fatal("browser fetch should stay off by default")
	}
	if !strings.Contains(resp.Context, "未能获取网页内容") {
		t.Fatalf("Context = %q", resp.Context)
	}
}

func TestResolverOnlyHandlesEnabledPlatforms(t *testing.T) {
	plugin := NewResolverPlugin(&http.Client{})
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text: "看 https://www.bilibili.com/video/BV1 和 https://weibo.com/123 和 https://www.xiaohongshu.com/1",
		Settings: SettingValues{
			"fetch_title":       false,
			"enabled_platforms": []any{"bilibili", "xiaohongshu"},
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !strings.Contains(resp.Context, "Bilibili") {
		t.Fatalf("bilibili missing: %q", resp.Context)
	}
	// 平台键精确匹配，启用 xiaohongshu 不会同时启用 x。
	if !strings.Contains(resp.Context, "小红书") {
		t.Fatalf("xiaohongshu should stay: %q", resp.Context)
	}
	if strings.Contains(resp.Context, "weibo") {
		t.Fatalf("weibo should be disabled: %q", resp.Context)
	}

	// 空列表表示所有已知平台均停用。
	resp, err = plugin.Handle(context.Background(), PluginRequest{
		Text:     "https://weibo.com/456",
		Settings: SettingValues{"fetch_title": false, "enabled_platforms": []string{}},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("all-disabled should return nil, got %q", resp.Context)
	}
}

// TestResolverCacheDisabledByZeroTTL 验证对应功能场景。
func TestResolverCacheDisabledByZeroTTL(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`<html><head><title>标题</title></head></html>`))
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	settings := SettingValues{"cache_ttl_minutes": 0}
	for i := 0; i < 2; i++ {
		if _, err := plugin.Handle(context.Background(), PluginRequest{Text: server.URL, Settings: settings}); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (cache disabled)", hits)
	}

	// 默认设置：第二次命中缓存不再请求。
	plugin = NewResolverPlugin(server.Client())
	hits = 0
	for i := 0; i < 2; i++ {
		if _, err := plugin.Handle(context.Background(), PluginRequest{Text: server.URL}); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 (cache enabled)", hits)
	}
}

// TestResolverSummaryLengthSetting 验证对应功能场景。
func TestResolverSummaryLengthSetting(t *testing.T) {
	long := strings.Repeat("云", 200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>标题</title><meta name="description" content="` + long + `"></head></html>`))
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:     server.URL,
		Settings: SettingValues{"summary_max_runes": 80},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	line := ""
	for _, l := range strings.Split(resp.Context, "\n") {
		if strings.Contains(l, "摘要：") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "摘要："))
		}
	}
	if got := len([]rune(line)); got != 81 { // 80 字 + 截断省略号
		t.Fatalf("summary runes = %d, want 81: %q", got, line)
	}
}

// TestMetaTagContentParsesAttributeOrders 验证对应功能场景。
func TestMetaTagContentParsesAttributeOrders(t *testing.T) {
	html := `<meta name='description' content='单引号描述'><meta content="先内容" property="og:title">`
	if got := metaTagContent(html, "og:title"); got != "先内容" {
		t.Fatalf("og:title = %q", got)
	}
	if got := metaTagContent(html, "description"); got != "单引号描述" {
		t.Fatalf("description = %q", got)
	}
}
