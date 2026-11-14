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
	"time"
)

func TestDeepSeekSearchSendsCountAndPrefersSummary(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer sk-ds" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"results":[{"title":"D","url":"https://example.com/d","snippet":"short","summary":"deepseek summary","date":"2026-11-13T00:00:00Z","authority":3},{"title":"E","url":"https://example.com/e","snippet":"only snippet","summary":"","date":null}]}`))
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "ds", Type: "deepseek", URL: server.URL, MaxResults: 3}}},
		APIKeys: map[string]string{"ds": "sk-ds"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "release"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "deepseek summary") || strings.Contains(output, `"short"`) || !strings.Contains(output, "only snippet") || strings.Contains(output, "sk-ds") {
		t.Fatalf("output = %s", output)
	}
	if body["query"] != "release" || body["count"] != float64(3) {
		t.Fatalf("body = %v", body)
	}
	config, err := NormalizeWebSearchConfig(WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "d", Type: "deepseek"}}})
	if err != nil || config.Providers[0].URL != DeepSeekSearchURL {
		t.Fatalf("config = %+v err=%v", config, err)
	}
}

func TestDeepSeekFetchURLFor(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.deepseek.com/v1/tools/search": "https://api.deepseek.com/v1/tools/fetch",
		"https://proxy.example/v1/tools/search/":   "https://proxy.example/v1/tools/fetch",
		"https://proxy.example/custom":             DeepSeekFetchURL,
		"":                                         DeepSeekFetchURL,
	} {
		if got := DeepSeekFetchURLFor(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestDeepSeekFetchReadsMarkdownLinksAndImages(t *testing.T) {
	resetTinyFishCache(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "Bearer sk-ds" || body["url"] != tinyFishTestPage {
			t.Errorf("auth=%q body=%v", r.Header.Get("Authorization"), body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"title": " Intro ", "content": "# 标题\n![logo](https://1.1.1.1/a.png)\n见 [下一页](https://1.1.1.1/next#x) 和 [下一页](https://1.1.1.1/next)，[相对](/rel)"})
	}))
	defer server.Close()
	page, err := fetchDeepSeekPage(context.Background(), "sk-ds", server.URL, tinyFishTestPage, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Intro" || page.SourceType != "deepseek_fetch" || len(page.Links) != 1 || page.Links[0].URL != "https://1.1.1.1/next" || len(page.Images) != 1 || page.Images[0].URL != "https://1.1.1.1/a.png" {
		t.Fatalf("page = %#v", page)
	}
	if _, err := fetchDeepSeekPage(context.Background(), "sk-ds", server.URL, tinyFishTestPage, 1000); err != nil || calls.Load() != 1 {
		t.Fatalf("cache miss: calls=%d err=%v", calls.Load(), err)
	}
}

func TestDeepSeekFetchFailuresAreRecognizable(t *testing.T) {
	resetTinyFishCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no usable content","type":"invalid_request_error"}}`))
	}))
	defer server.Close()
	_, err := fetchDeepSeekPage(context.Background(), "sk-ds", server.URL, tinyFishTestPage, 1000)
	if !errors.Is(err, errDeepSeekFetch) || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no usable content") {
		t.Fatalf("err = %v", err)
	}
	if _, err := fetchDeepSeekPage(context.Background(), "", server.URL, tinyFishTestPage, 1000); !errors.Is(err, errDeepSeekFetch) {
		t.Fatalf("missing key err = %v", err)
	}
}

// TinyFish 失败时接着试 DeepSeek，DeepSeek 成功就不起 Chrome。
func TestSandboxedBrowserFallsBackFromTinyFishToDeepSeek(t *testing.T) {
	resetTinyFishCache(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer failing.Close()
	deepseek := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "Docs", "content": "from deepseek"})
	}))
	defer deepseek.Close()
	browser := NewSandboxedBrowser(SandboxedBrowserConfig{Executable: "/nonexistent/chrome", FetchAPIKey: "tf", FetchEndpoint: failing.URL, DeepSeekFetchKey: "sk-ds", DeepSeekFetchEndpoint: deepseek.URL})
	page, err := browser.Render(context.Background(), tinyFishTestPage)
	if err != nil || page.Text != "from deepseek" || page.BrowserEngine != "deepseek" {
		t.Fatalf("page=%#v err=%v", page, err)
	}

	resetTinyFishCache(t)
	browser = NewSandboxedBrowser(SandboxedBrowserConfig{Executable: "/nonexistent/chrome", DeepSeekFetchKey: "sk-ds", DeepSeekFetchEndpoint: failing.URL})
	_, err = browser.Render(context.Background(), tinyFishTestPage)
	if err == nil || !errors.Is(err, errDeepSeekFetch) || !strings.Contains(err.Error(), "/nonexistent/chrome") {
		t.Fatalf("fallback err = %v", err)
	}
}
