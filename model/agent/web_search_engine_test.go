// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func searchEngineTestTool(t *testing.T, renderer PageRenderer, engines ...string) *WebSearchTool {
	t.Helper()
	providers := make([]WebSearchProviderConfig, 0, len(engines))
	for _, engine := range engines {
		providers = append(providers, WebSearchProviderConfig{Name: engine, Type: WebSearchProviderSearchEngine, Tool: engine})
	}
	tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: providers}, Renderer: renderer})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestSearchEngineModeReadsResultsFromResultPage(t *testing.T) {
	var requested []string
	renderer := PageRendererFunc(func(_ context.Context, rawURL string) (RenderedPage, error) {
		requested = append(requested, rawURL)
		return RenderedPage{
			URL:   rawURL,
			Title: "diana - Google 搜索",
			Text:  "Diana 发布说明 github.com › SuInk › Diana 这里是摘要",
			Links: []RenderedLink{
				{URL: "https://accounts.google.com/signin", Text: "登录"},
				{URL: "https://www.google.com/url?q=https://github.com/SuInk/Diana/releases&sa=U", Text: "Diana 发布说明\ngithub.com › SuInk"},
				{URL: "https://example.com/post?utm_source=x", Text: "一篇文章"},
				{URL: "https://example.com/post", Text: "同一篇文章的重复链接"},
			},
		}, nil
	})
	output, err := searchEngineTestTool(t, renderer, "google").Run(context.Background(), map[string]any{"query": "diana 最新版本"})
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 {
		t.Fatalf("requested = %v", requested)
	}
	parsed, _ := url.Parse(requested[0])
	if parsed.Host != "www.google.com" || parsed.Query().Get("q") != "diana 最新版本" {
		t.Fatalf("search URL = %s", requested[0])
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.Provider != "google" {
		t.Fatalf("status=%s provider=%s", result.Status, result.Provider)
	}
	want := []string{"https://github.com/SuInk/Diana/releases", "https://example.com/post"}
	if strings.Join(result.Sources, " ") != strings.Join(want, " ") {
		t.Fatalf("sources = %v", result.Sources)
	}
	if len(result.Results) != 2 || result.Results[0].Title != "Diana 发布说明" || !strings.Contains(result.Results[0].Snippet, "这里是摘要") {
		t.Fatalf("content = %s", result.Content)
	}
	if strings.Contains(result.Content, "google.com/search") {
		t.Fatalf("结果页地址不该进输出，会被当成来源: %s", result.Content)
	}
}

func TestSearchEngineModeFallsBackIndependentlyForParallelQueries(t *testing.T) {
	calls := map[string]int{}
	var callsMu sync.Mutex
	renderer := PageRendererFunc(func(_ context.Context, rawURL string) (RenderedPage, error) {
		parsed, _ := url.Parse(rawURL)
		callsMu.Lock()
		calls[parsed.Host]++
		callsMu.Unlock()
		if parsed.Host == "www.google.com" {
			return RenderedPage{URL: "https://www.google.com/sorry/index?continue=x", Title: "Google", Text: "Our systems have detected unusual traffic"}, nil
		}
		// 第一个候选词在 Bing 上搜不到，逼搜索换第二个候选词，看 Google 会不会被再试一次。
		if parsed.Query().Get("q") == "first" {
			return RenderedPage{URL: rawURL, Title: "Bing", Text: "没有结果"}, nil
		}
		target := base64.RawURLEncoding.EncodeToString([]byte("https://example.org/answer"))
		return RenderedPage{URL: rawURL, Title: "Bing", Text: "answer", Links: []RenderedLink{
			{URL: "https://www.bing.com/ck/a?!&&p=abc&u=a1" + target + "&ntb=1", Text: "答案页"},
		}}, nil
	})
	output, err := searchEngineTestTool(t, renderer, "google", "bing").Run(context.Background(), map[string]any{"query": "first", "queries": []any{"second"}})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.Provider != "bing" || len(result.Sources) != 1 || result.Sources[0] != "https://example.org/answer" {
		t.Fatalf("result = %+v", result)
	}
	if calls["www.google.com"] != 2 || calls["www.bing.com"] != 2 {
		t.Fatalf("calls = %v", calls)
	}
}

// 搜「异常流量」本身时结果页正文里就有这几个字，取到了结果就不能当成被拦。
func TestSearchEngineBlockMarkerInBodyNeedsEmptyResults(t *testing.T) {
	page := RenderedPage{URL: "https://www.google.com/search?q=x", Title: "异常流量 - Google 搜索", Text: "什么是 unusual traffic"}
	engine := searchEngines["google"]
	if searchEnginePageBlocked(engine, RenderedPage{URL: page.URL, Title: "x - Google 搜索", Text: page.Text}, true) {
		t.Fatal("有结果时正文标记不该算被拦")
	}
	if !searchEnginePageBlocked(engine, RenderedPage{URL: page.URL, Title: "Google", Text: page.Text}, false) {
		t.Fatal("没有结果、正文带验证提示应当算被拦")
	}
}

func TestRenderedPageKeepsVisibleLinks(t *testing.T) {
	page, err := parseRenderedPage([]byte(`<html><body><a href="/a"><h3>标题</h3><cite>example.com</cite></a><a href="javascript:void(0)">x</a><div hidden><a href="/h">藏起来</a></div></body></html>`), "https://example.com/s", 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Links) != 1 || page.Links[0].URL != "https://example.com/a" || firstRenderedLine(page.Links[0].Text) != "标题" {
		t.Fatalf("links = %#v", page.Links)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "example.com/a") {
		t.Fatalf("links 不该出现在 browser_render 的输出里: %s", encoded)
	}
}

func TestSearchEngineResultsSkipDisplayURLsAndResolveOpaqueRedirects(t *testing.T) {
	redirects := map[string]string{
		"/goto?url=AAA": "https://example.com/real",
		"/goto?url=BBB": "https://example.com/real",
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		location := redirects[req.URL.RequestURI()]
		if location == "" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: req}, nil
	})
	renderer := PageRendererFunc(func(_ context.Context, rawURL string) (RenderedPage, error) {
		return RenderedPage{URL: rawURL, Title: "x - Google 搜索", Text: "全部 图片 视频 工具\n真正的标题\n摘要写在这里", Links: []RenderedLink{
			{URL: "https://other.org/", Text: "other.org"},
			{URL: "https://other.org/doc", Text: "https://other.org › doc"},
			{URL: "https://other.org/doc", Text: "别的文档"},
			{URL: "https://www.google.com/goto?url=AAA", Text: "真正的标题"},
			{URL: "https://www.google.com/goto?url=BBB", Text: "同一个地址的另一条"},
			{URL: "https://www.google.com/goto?url=CCC", Text: "解不出来的"},
		}}, nil
	})
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:   WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "google", Type: WebSearchProviderSearchEngine}}},
		Renderer: renderer,
		Client:   &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"https://other.org/doc", "https://example.com/real", "https://www.google.com/goto?url=CCC"}
	if strings.Join(result.Sources, " ") != strings.Join(want, " ") {
		t.Fatalf("sources = %v", result.Sources)
	}
	if strings.Contains(output, "全部 图片") || !strings.Contains(output, "摘要写在这里") {
		t.Fatalf("正文应当从第一条结果开始: %s", result.Content)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSearchEngineResultsUnwrapAndDeduplicateTranslationLinks(t *testing.T) {
	original := "https://docs.example.com/pricing?model=pro&region=cn"
	standard := "https://translate.google.com/translate?u=" + url.QueryEscape(original) + "&hl=zh-CN"
	escaped := "https://translate.google.com/translate?client=" + url.QueryEscape(`search\u0026hl=zh-CN\u0026u=`+url.QueryEscape(original))
	for _, translated := range []string{standard, escaped} {
		results := searchEngineResults(searchEngines["google"], []RenderedLink{
			{URL: original, Text: "Official pricing"},
			{URL: translated, Text: "Translate this page"},
			{URL: "https://example.org/release", Text: "Release notes"},
		}, 2)
		if len(results) != 2 || results[0].URL != original || results[1].URL != "https://example.org/release" {
			t.Fatalf("duplicate translation consumed source slot: %+v", results)
		}
		sources := webSearchResultSources(original + " " + translated)
		if len(sources) != 1 || sources[0] != original {
			t.Fatalf("translation duplicated source evidence: %v", sources)
		}
	}
}
