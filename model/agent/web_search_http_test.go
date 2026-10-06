// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestHTTPSearchGETAndPOSTMapResultsAndRespectLimits(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.Header.Get("X-Search-Key") != "private-key" || r.Header.Get("Authorization") != "" {
					t.Errorf("incorrect method/auth")
				}
				var params map[string]any
				if method == http.MethodPost {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("JSON content type missing")
					}
					json.NewDecoder(r.Body).Decode(&params)
					if params["q"] != "嘉然 & updates" || params["limit"] != float64(2) || params["language"] != "zh" {
						t.Errorf("params=%#v", params)
					}
				} else if r.URL.Query().Get("q") != "嘉然 & updates" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("language") != "zh" || r.URL.Query().Get("existing") != "keep" {
					t.Errorf("query=%s", r.URL.RawQuery)
				}
				w.Write([]byte(`{"data":{"items":[{"page":{"url":"javascript:alert(1)","name":"Bad"}},{"page":{"url":"https://example.org/a","name":"A"},"summaries":["First"]},{"page":{"url":"https://example.org/a","name":"Duplicate"}},{"page":{"url":"https://example.org/b","name":"B"},"summaries":["Second"]},{"page":{"url":"https://example.org/c","name":"Beyond limit"}}]}}`))
			}))
			defer server.Close()
			provider := WebSearchProviderConfig{Name: "custom", Type: "http", URL: server.URL + "?existing=keep", QueryParam: "q", ResultsParam: "limit", MaxResults: 2, HTTPConfig: &HTTPSearchConfig{Method: method, AuthType: "header", AuthHeader: "X-Search-Key", Params: map[string]any{"q": "must-be-overridden", "language": "zh"}, ResultsPath: "data.items", URLPath: "page.url", TitlePath: "page.name", SnippetPath: "summaries.0"}}
			result := ProbeWebSearchProvider(context.Background(), provider, "嘉然 & updates", WebSearchToolOptions{APIKeys: map[string]string{"custom": "private-key"}})
			if result.Error != "" || result.HTTPStatus != 200 || result.ResultCount != 2 || !strings.Contains(result.Content, "First") || strings.Contains(result.Content, "Beyond limit") || strings.Contains(result.Content, "private-key") {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestHTTPSearchStandardFormatAuthAndBodyKeyPlaceholder(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "global-key-must-not-be-used")
	for _, auth := range []string{"none", "bearer"} {
		t.Run(auth, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["query"] != "Diana" || body["count"] != float64(3) || body["token"] != "own-key" {
					t.Errorf("body=%#v", body)
				}
				if auth == "none" && r.Header.Get("Authorization") != "" || auth == "bearer" && r.Header.Get("Authorization") != "Bearer own-key" {
					t.Error("wrong auth")
				}
				w.Write([]byte(`[{"link":"https://example.org","title":"own-key","snippet":"Result"}]`))
			}))
			defer server.Close()
			provider := WebSearchProviderConfig{Name: "draft", Type: "http", URL: server.URL, ResultsParam: "count", MaxResults: 3, HTTPConfig: &HTTPSearchConfig{AuthType: auth, Params: map[string]any{"token": "{api_key}"}}}
			result := ProbeWebSearchProvider(context.Background(), provider, "Diana", WebSearchToolOptions{APIKeys: map[string]string{"draft": "own-key"}})
			if result.Error != "" || result.ResultCount != 1 || strings.Contains(result.Content, "own-key") || !strings.Contains(result.Content, "[redacted]") {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestHTTPSearchEmptyMalformedAndUnavailableFallBack(t *testing.T) {
	for _, response := range []string{`{"results":[]}`, `{"data":{}}`, `not JSON`, `{"results":[{"url":"file:///private"}]}`} {
		t.Run(response, func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(response)) }))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(`{"results":[{"url":"https://example.org/fallback","title":"Fallback","content":"Found"}]}`))
			}))
			defer fallback.Close()
			tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "primary", Type: "http", URL: primary.URL}, {Name: "fallback", Type: "http", URL: fallback.URL}}}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := tool.Run(context.Background(), map[string]any{"query": "Diana"})
			if err != nil || !strings.Contains(out, `"fallback_used": true`) || !strings.Contains(out, "example.org/fallback") {
				t.Fatalf("out=%s err=%v", out, err)
			}
		})
	}
}

func TestHTTPSearchRejectsInvalidConfigAndDoesNotMutateParams(t *testing.T) {
	for _, cfg := range []HTTPSearchConfig{{Method: "PUT"}, {AuthType: "unknown"}, {AuthType: "header", AuthHeader: "X-Key\r\nInjected"}, {AuthType: "header", AuthHeader: "Host"}, {ResultsPath: "$.items[*]"}, {URLPath: "page..url"}} {
		_, err := NormalizeWebSearchConfig(WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "custom", Type: "http", URL: "https://example.org/search", HTTPConfig: &cfg}}})
		if err == nil {
			t.Fatalf("accepted invalid config=%#v", cfg)
		}
	}
	params := map[string]any{"nested": map[string]any{"key": "old"}}
	config, err := NormalizeWebSearchConfig(WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "custom", Type: "http", URL: "https://example.org/search", HTTPConfig: &HTTPSearchConfig{Params: params}}}})
	if err != nil {
		t.Fatal(err)
	}
	config.Providers[0].HTTPConfig.Params["nested"].(map[string]any)["key"] = "new"
	if params["nested"].(map[string]any)["key"] != "old" {
		t.Fatal("snapshot shares request parameter map")
	}
}

func TestHTTPSearchProbeReportsStatusWithoutEchoingRemoteBodyOrFollowingRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, status := range []int{http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
			w.Write([]byte("echo secret-test-token"))
		}))
		result := ProbeWebSearchProvider(context.Background(), WebSearchProviderConfig{Name: "draft", Type: "http", URL: server.URL, HTTPConfig: &HTTPSearchConfig{AuthType: "bearer"}}, "Diana", WebSearchToolOptions{APIKeys: map[string]string{"draft": "secret-test-token"}})
		server.Close()
		if result.HTTPStatus != status || result.Error == "" || strings.Contains(result.Error, "secret-test-token") || result.Content != "" {
			t.Fatalf("result=%+v", result)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("custom search followed a redirect")
	}
}

// Uses the currently configured production model with an isolated search API.
// No bot messages are sent and no production provider settings are changed.
func TestLiveHTTPSearchCustomAdapter(t *testing.T) {
	client := liveAgentClient(t)
	const record = "DIANA-HTTP-SEARCH-REPLAY-4827"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Replay-Key") != "replay-fixture-key" {
			t.Error("runtime did not use the configured custom HTTP adapter")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": []map[string]string{{"href": "https://example.org/diana-http-search-verification", "name": "Diana HTTP 搜索配置验证记录", "summary": "这是隔离验证夹具，记录编号为 " + record + "。"}}}})
	}))
	defer server.Close()
	search, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "custom-http", Type: "http", URL: server.URL, ResultsParam: "count", HTTPConfig: &HTTPSearchConfig{AuthType: "header", AuthHeader: "X-Replay-Key", ResultsPath: "data.items", URLPath: "href", TitlePath: "name", SnippetPath: "summary"}}}}, APIKeys: map[string]string{"custom-http": "replay-fixture-key"}, MaxQueries: 1, MaxProviderCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(client, Config{MaxSteps: 3, ToolTimeoutMS: 30000}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	response, err := runner.Run(ctx, Request{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "你是嘉然。使用 web_search 查询用户要核对的记录，依据返回的摘要回答，简洁中文。"}, {Role: llm.RoleUser, Content: "查一下 Diana HTTP 搜索配置验证记录，把记录编号告诉我。"}}})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 || !strings.Contains(response.Text, record) || strings.Contains(response.Text, "replay-fixture-key") {
		t.Fatalf("adapter calls=%d, reply=%s", calls.Load(), response.Text)
	}
	t.Logf("model=%s adapter_calls=%d reply=%s", response.Model, calls.Load(), response.Text)
}
