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

func TestNewWebSearchToolRejectsUnsafeRemoteURL(t *testing.T) {
	_, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{
		Name: "unsafe", Type: "tavily", URL: "http://example.com/search",
	}}}})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("error = %v", err)
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
