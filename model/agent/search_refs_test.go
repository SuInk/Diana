// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestBrowserRenderAcceptsSearchRef(t *testing.T) {
	const target = "https://blog.example.com/launch"
	ref := searchRef(target)
	var read []string
	renderer := PageRendererFunc(func(_ context.Context, raw string) (RenderedPage, error) {
		read = append(read, raw)
		return RenderedPage{RequestedURL: raw, URL: raw, Title: "Launch", Text: "launched"}, nil
	})
	search := &recordingSearchTool{output: `{"status":"ok","results":[{"url":"` + target + `","title":"Launch"}]}`}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"launch"}}`,
		`{"action":"tool","tool":"browser_render","input":{"url":"` + ref + `"}}`,
		`{"action":"final","content":"done"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(search, NewBrowserRenderTool(renderer)))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查"}}}); err != nil {
		t.Fatal(err)
	}
	if len(read) != 1 || read[0] != target {
		t.Fatalf("ref not resolved: %v", read)
	}
}

func TestSearchRefStableAndResultsCarryIt(t *testing.T) {
	if searchRef("https://example.com/a?utm_source=x") != searchRef("https://example.com/a") || !strings.HasPrefix(searchRef("https://example.com/a"), "r") {
		t.Fatal("ref should follow canonical URL")
	}
	tool := &WebSearchTool{}
	raw, err := tool.formatExplorationResult(webSearchResult{Status: "ok", Results: []WebSearchHit{{URL: "https://example.com/a"}}})
	if err != nil || !strings.Contains(raw, `"ref": "`+searchRef("https://example.com/a")+`"`) {
		t.Fatalf("ref missing: %s %v", raw, err)
	}
	if got := resolveSearchRefs(browserRenderToolName, map[string]any{"url": "rzzzzzz"}, nil)["url"]; got != "rzzzzzz" {
		t.Fatalf("unknown ref changed: %v", got)
	}
}
