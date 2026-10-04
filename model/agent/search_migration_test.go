package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestSearchQueriesStartConcurrentlyAndKeepPartialResults(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	renderer := PageRendererFunc(func(ctx context.Context, raw string) (RenderedPage, error) {
		u, _ := url.Parse(raw)
		query := u.Query().Get("q")
		started <- query
		select {
		case <-release:
		case <-ctx.Done():
			return RenderedPage{}, ctx.Err()
		}
		if query == "unavailable" {
			return RenderedPage{}, errors.New("provider unavailable")
		}
		return RenderedPage{URL: raw, Title: "Search", Text: "Source\nThe useful snippet", Links: []RenderedLink{{URL: "https://example.com/docs", Text: "Source", Snippet: "The useful snippet"}}}, nil
	})
	tool := searchEngineTestTool(t, renderer, "google")
	done := make(chan string, 1)
	go func() {
		raw, err := tool.Run(context.Background(), map[string]any{"queries": []string{"unavailable", "working"}})
		if err != nil {
			done <- err.Error()
			return
		}
		done <- raw
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("independent queries did not start concurrently")
		}
	}
	close(release)
	raw := <-done
	var result webSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || len(result.Searches) != 2 || result.Searches[0].Status != "provider_error" || result.Searches[1].Status != "ok" || len(result.Results) != 1 || result.Results[0].Snippet != "The useful snippet" {
		t.Fatalf("partial results lost: %s", raw)
	}
}

func TestRunnerLetsModelChooseAndChangeResearchPages(t *testing.T) {
	var mu sync.Mutex
	var read []string
	renderer := PageRendererFunc(func(_ context.Context, raw string) (RenderedPage, error) {
		mu.Lock()
		defer mu.Unlock()
		read = append(read, raw)
		return RenderedPage{RequestedURL: raw, URL: raw, Text: "Original content", Title: "Official page"}, nil
	})
	search := &recordingSearchTool{output: `{"status":"ok","sources":["https://example.com/home","https://example.com/docs"]}`}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"entity docs"}}`,
		`{"action":"tool","tool":"browser_render","input":{"url":"https://example.com/docs","find":"feature"}}`,
		`{"action":"tool","tool":"web_search","input":{"query":"entity release record"}}`,
		`{"action":"final","content":"来源：https://example.com/docs。另一项尚未确认。"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 6}, NewToolRegistry(search, NewBrowserRenderTool(renderer)))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查两个问题"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 4 || len(read) != 1 || read[0] != "https://example.com/docs" {
		t.Fatalf("model choice changed: requests=%d reads=%v", len(client.requests), read)
	}
	for _, req := range client.requests {
		if req.ToolChoice != "" {
			t.Fatalf("forced research tool: %s", req.ToolChoice)
		}
		raw, _ := json.Marshal(req.Tools)
		if strings.Contains(string(raw), `"claims"`) {
			t.Fatal("old evidence protocol still exposed")
		}
	}
	if len(response.Sources) != 2 || !response.Sources[0].Cited || !response.Sources[0].Read {
		t.Fatalf("passive source recall lost: %+v", response.Sources)
	}
}

func TestPassiveSourcesDoNotPromoteInventedCitations(t *testing.T) {
	sources := responseSources("来源：https://invented.example/page", []Step{{Tool: WebSearchToolName, Output: `{"status":"ok","sources":["https://actual.example/page"]}`}})
	if len(sources) != 1 || sources[0].Cited || sources[0].Read {
		t.Fatalf("invented citation became retrieved/read: %+v", sources)
	}
}

func TestSearchRedirectDecodesEscapedSeparators(t *testing.T) {
	got := unwrapSearchEngineLink(`https://www.google.com/url?q=https%3A%2F%2Fexample.com%2Fdocs\u0026sa=X\u0026ved=abc`)
	if got != "https://example.com/docs" {
		t.Fatalf("tracking escaped into target: %s", got)
	}
	got = unwrapSearchEngineLink(`https://www.google.com/url?q=https%3A%2F%2Fexample.com%2Fdocs%5Cu0026sa=X%5Cu0026ved=abc`)
	if got != "https://example.com/docs" {
		t.Fatalf("encoded tracking escaped into target: %s", got)
	}
}

func TestParallelSearchLargeResultsFinishWithinOutputBudget(t *testing.T) {
	result := webSearchResult{Status: "ok", Strategy: "parallel_queries"}
	for i := 0; i < 32; i++ {
		result.Results = append(result.Results, WebSearchHit{URL: "https://example.com/" + strings.Repeat("path", i+1), Title: strings.Repeat("title", 100), Snippet: strings.Repeat("长摘要<>\"", 200)})
	}
	done := make(chan string, 1)
	go func() {
		raw, err := (&WebSearchTool{maxBytes: 2000}).formatExplorationResult(result)
		if err != nil {
			done <- err.Error()
		} else {
			done <- raw
		}
	}()
	select {
	case raw := <-done:
		var got webSearchResult
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Truncated || len([]rune(raw)) > 2000 || got.Status != "ok" {
			t.Fatalf("invalid bounded result: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("search result formatting failed to terminate")
	}
}
