package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWebSearchToolFallsBackToTavilyWithoutLeakingKey(t *testing.T) {
	exa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer exa.Close()

	const apiKey = "secret-tavily-key"
	var authorization string
	tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Official","url":"https://example.com/release","content":"released today","score":0.99}]}`))
	}))
	defer tavily.Close()

	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config: WebSearchConfig{Providers: []WebSearchProviderConfig{
			{Name: "exa", Type: "exa_mcp", URL: exa.URL, Tool: "web_search_exa", TimeoutMS: 2_000},
			{Name: "tavily", Type: "tavily", URL: tavily.URL, TimeoutMS: 2_000},
		}},
		APIKeys: map[string]string{"tavily": apiKey},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "latest release"})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer "+apiKey {
		t.Fatalf("Authorization = %q", authorization)
	}
	if !strings.Contains(output, `"provider": "tavily"`) || !strings.Contains(output, "example.com/release") {
		t.Fatalf("output = %s", output)
	}
	if strings.Contains(output, apiKey) {
		t.Fatalf("API key leaked in output: %s", output)
	}
}

func TestWebSearchToolCallsExaMCP(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			Method string `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeTestMCPEvent(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}}}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			query, _ = request.Params.Arguments["query"].(string)
			writeTestMCPEvent(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"Official result https://example.com"}],"isError":false}}`)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{
		Name: "exa", Type: "exa_mcp", URL: server.URL, Tool: "web_search_exa", TimeoutMS: 2_000,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "Diana release"})
	if err != nil {
		t.Fatal(err)
	}
	if query != "Diana release" || !strings.Contains(output, "Official result") {
		t.Fatalf("query=%q output=%s", query, output)
	}
}

func TestWebSearchToolExploresModelCandidatesAfterNoResults(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		queries = append(queries, payload.Query)
		w.Header().Set("Content-Type", "application/json")
		if payload.Query == "overly precise phrase" {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"Official","url":"https://example.com/found","content":"verified"}]}`))
	}))
	defer server.Close()

	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{
			Name: "search", Type: "tavily", URL: server.URL, TimeoutMS: 2_000,
		}}},
		APIKeys: map[string]string{"search": "test-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{
		"query":   "overly precise phrase",
		"queries": []any{"broader alias"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.SelectedQuery != "broader alias" || !result.FallbackUsed {
		t.Fatalf("result = %#v", result)
	}
	if strings.Join(queries, ",") != "overly precise phrase,broader alias" {
		t.Fatalf("queries = %#v", queries)
	}
	if len(result.Attempts) != 2 || result.Attempts[0].Status != "no_results" || result.Attempts[1].Status != "success" {
		t.Fatalf("attempts = %#v", result.Attempts)
	}
}

func TestWebSearchToolRelaxesStrictQuotesWithoutDomainRules(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		queries = append(queries, payload.Query)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(payload.Query, `"`) {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"Match","url":"https://example.com/match","content":"found"}]}`))
	}))
	defer server.Close()

	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "search", Type: "tavily", URL: server.URL}}},
		APIKeys: map[string]string{"search": "test-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": `"strict phrase"`})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || len(queries) != 2 || queries[1] != "strict phrase" {
		t.Fatalf("result=%#v queries=%#v", result, queries)
	}
	if len(result.Queries) < 2 || result.Queries[1].Strategy != "quotes_relaxed" {
		t.Fatalf("candidates = %#v", result.Queries)
	}
}

func TestWebSearchToolReportsSkippedAttemptedAndNotExecutedProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Result","url":"https://example.com/result","content":"verified"}]}`))
	}))
	defer server.Close()

	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config: WebSearchConfig{Providers: []WebSearchProviderConfig{
			{Name: "disabled", Type: "tavily", URL: server.URL, Disabled: true},
			{Name: "active", Type: "tavily", URL: server.URL},
			{Name: "spare", Type: "tavily", URL: server.URL},
		}},
		APIKeys: map[string]string{"active": "test-key", "spare": "test-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "answer"})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 3 || result.Providers[0].Status != "skipped" || result.Providers[1].Status != "attempted" || result.Providers[2].Status != "not_executed" {
		t.Fatalf("providers = %#v", result.Providers)
	}
	if result.Providers[2].Reason != "sufficient_evidence" {
		t.Fatalf("spare provider = %#v", result.Providers[2])
	}
}

func TestWebSearchToolStopsAtProviderCallBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:           WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "search", Type: "tavily", URL: server.URL}}},
		APIKeys:          map[string]string{"search": "test-key"},
		MaxProviderCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "one", "queries": []any{"two", "three"}})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "budget_exhausted" || result.Budget.ProviderCalls != 1 || result.Queries[1].Status != "not_executed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestWebSearchToolReturnsStructuredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "search", Type: "tavily", URL: server.URL}}},
		APIKeys: map[string]string{"search": "test-key"},
		Timeout: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "slow"})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "timeout" || len(result.Attempts) != 1 || result.Attempts[0].Status != "timeout" {
		t.Fatalf("result = %#v", result)
	}
}

func TestWebSearchToolDistinguishesTerminalStatuses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantStatus string
	}{
		{name: "no results", statusCode: http.StatusOK, body: `{"results":[]}`, wantStatus: "no_results"},
		{name: "provider error", statusCode: http.StatusServiceUnavailable, body: `unavailable`, wantStatus: "provider_error"},
		{name: "insufficient evidence", statusCode: http.StatusOK, body: `{"results":[{"title":"Claim","url":"","content":"no verifiable source"}]}`, wantStatus: "insufficient_evidence"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			tool, err := NewWebSearchTool(WebSearchToolOptions{
				Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "search", Type: "tavily", URL: server.URL}}},
				APIKeys: map[string]string{"search": "test-key"},
			})
			if err != nil {
				t.Fatal(err)
			}
			output, err := tool.Run(context.Background(), map[string]any{"query": "plain"})
			if err != nil {
				t.Fatal(err)
			}
			var result webSearchResult
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus || len(result.Attempts) != 1 || result.Attempts[0].Status != test.wantStatus {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestWebSearchToolKeepsStructuredOutputWithinLimit(t *testing.T) {
	longContent := strings.Repeat("evidence ", 2_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"results":[{"title":"Result","url":"https://example.com/result","content":%q}]}`, longContent)
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:         WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "search", Type: "tavily", URL: server.URL}}},
		APIKeys:        map[string]string{"search": "test-key"},
		MaxOutputChars: 2_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "bounded"})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(output)) > 2_000 {
		t.Fatalf("output runes = %d", len([]rune(output)))
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("truncated output is not valid JSON: %v\n%s", err, output)
	}
	if result.Status != "ok" || result.Content == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestWebSearchMetadataDoesNotContainRawPrivateQuery(t *testing.T) {
	input := map[string]any{"query": "person@example.com token=secret-value private question"}
	metadata := webSearchRunMetadataFromInput(WebSearchToolName, input)
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "person@example.com") || strings.Contains(text, "secret-value") || strings.Contains(text, "private question") {
		t.Fatalf("metadata leaked query: %s", text)
	}
	if !strings.Contains(text, `"hash"`) || !strings.Contains(text, `"constraint_count"`) {
		t.Fatalf("metadata missing safe diagnostics: %s", text)
	}
}

func TestWebSearchSourcesDeduplicateTrackingVariants(t *testing.T) {
	sources := webSearchResultSources("https://Example.com/article?utm_source=a&id=1#top https://example.com/article?id=1&utm_medium=b https://other.example/article")
	if len(sources) != 2 || sources[0] != "https://example.com/article?id=1" || sources[1] != "https://other.example/article" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestNewWebSearchToolRejectsUnsafeRemoteURL(t *testing.T) {
	_, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{
		Name: "unsafe", Type: "tavily", URL: "http://example.com/search",
	}}}})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractMCPToolTextPreservesCompleteErrors(t *testing.T) {
	message := strings.Repeat("provider-error-", 40) + "tail-marker"
	raw, err := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = extractMCPToolText(raw)
	if err == nil {
		t.Fatal("expected MCP tool error")
	}
	want := "MCP tool reported an error: " + message
	if err.Error() != want {
		t.Fatalf("error length = %d, want %d", len(err.Error()), len(want))
	}
}

func TestWebSearchToolLiveExaMCP(t *testing.T) {
	if os.Getenv("DIANA_LIVE_WEB_SEARCH") != "1" {
		t.Skip("set DIANA_LIVE_WEB_SEARCH=1 to run a real Exa MCP search")
	}
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:  DefaultWebSearchConfig(),
		Timeout: 25 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{
		"query": "Exa official MCP documentation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "https://") {
		t.Fatalf("live search returned no source URL: %s", output)
	}
}

func writeTestMCPEvent(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
}
