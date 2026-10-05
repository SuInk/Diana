// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestCustomSearchMCPUsesConfiguredToolAndArguments(t *testing.T) {
	for _, countParam := range []string{"limit", ""} {
		t.Run("count="+countParam, func(t *testing.T) {
			var actual map[string]any
			var name, auth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				switch req.Method {
				case "initialize":
					writeTestMCPEvent(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}}}}`)
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/call":
					actual, name, auth = req.Params.Arguments, req.Params.Name, r.Header.Get("Authorization")
					writeTestMCPEvent(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"source https://example.org/release"}]}}`)
				}
			}))
			defer server.Close()
			tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "custom", Type: "search_mcp", URL: server.URL, Tool: "lookup_news", QueryParam: "search_text", ResultsParam: countParam, MaxResults: 3}}}, APIKeys: map[string]string{"custom": "test-custom-key"}})
			if err != nil {
				t.Fatal(err)
			}
			output, err := tool.Run(context.Background(), map[string]any{"query": "Diana & updates"})
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]any{"search_text": "Diana & updates"}
			if countParam != "" {
				expected[countParam] = float64(3)
			}
			if !reflect.DeepEqual(actual, expected) || name != "lookup_news" || auth != "Bearer test-custom-key" {
				t.Fatalf("name=%s args=%#v auth=%s", name, actual, auth)
			}
			if strings.Contains(output, "test-custom-key") {
				t.Fatal("key leaked")
			}
		})
	}
}

func TestBrowserSearchEncodesQueryAndExtractsPublicResultLinks(t *testing.T) {
	renderer := PageRendererFunc(func(_ context.Context, raw string) (RenderedPage, error) {
		u, err := url.Parse(raw)
		if err != nil || u.Query().Get("text") != "嘉然 & release" || u.Query().Get("lang") != "zh" {
			t.Fatalf("query URL=%s", raw)
		}
		return parseRenderedPage([]byte(`<html><body><a href="/settings">Settings</a><a hidden href="https://hidden.example">Hidden</a><a href="/url?url=https%3A%2F%2Fexample.org%2Frelease"><h3>Official release</h3></a><a href="https://example.org/release">Duplicate</a><a href="https://news.example/article">News</a><a href="https://third.example">Third</a></body></html>`), raw, 10000, false)
	})
	tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "browser", Type: "browser", URL: "https://engine.example/search?lang=zh", QueryParam: "text", MaxResults: 2}}}, Renderer: renderer})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "嘉然 & release"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "example.org/release") || !strings.Contains(output, "news.example/article") || strings.Contains(output, "hidden.example") || strings.Contains(output, "third.example") {
		t.Fatalf("output=%s", output)
	}
}

func TestBrowserSearchVerificationFallsBackToCustomAPI(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "global-key-must-not-leak")
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"Source","url":"https://example.org","content":"Found"}]}`))
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "browser", Type: "browser"}, {Name: "private-api", Type: "tavily", URL: server.URL, NoEnvAPIKey: true}}}, APIKeys: map[string]string{"private-api": "private-test-key"}, Renderer: PageRendererFunc(func(context.Context, string) (RenderedPage, error) {
		return RenderedPage{Title: "Verify you are human"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "release"})
	if err != nil || !strings.Contains(output, `"provider": "private-api"`) || auth != "Bearer private-test-key" {
		t.Fatalf("output=%s err=%v auth=%s", output, err, auth)
	}
}

func TestCustomSearchRejectsAmbiguousParameters(t *testing.T) {
	_, err := NormalizeWebSearchConfig(WebSearchConfig{Providers: []WebSearchProviderConfig{{Type: "search_mcp", URL: "https://example.org/mcp", Tool: "search", QueryParam: "q", ResultsParam: "q"}}})
	if err == nil {
		t.Fatal("overlapping query/count parameter accepted")
	}
}

func TestCustomSearchMCPRedactsCredentialEchoedInRemoteErrors(t *testing.T) {
	const secret = "custom-secret-echoed-by-server"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			writeTestMCPEvent(w, `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			writeTestMCPEvent(w, `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"invalid token custom-secret-echoed-by-server"}}`)
		}
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "custom", Type: "search_mcp", URL: server.URL, Tool: "search"}}}, APIKeys: map[string]string{"custom": secret}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "release"})
	if strings.Contains(output, secret) || err != nil && strings.Contains(err.Error(), secret) {
		t.Fatalf("credential echoed to model: %s %v", output, err)
	}
	if !strings.Contains(output, "[redacted]") {
		t.Fatalf("error not captured: %s %v", output, err)
	}
}
