// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Over-budget output must give up diagnostics before evidence: the snippets
// are what the model checks its answer against.
func TestWebSearchTruncationKeepsSnippetsOverMetadata(t *testing.T) {
	result := webSearchResult{Status: "ok", Strategy: "parallel_queries", Budget: webSearchBudget{MaxQueries: 4, QueriesUsed: 4}}
	for i := 0; i < 8; i++ {
		url := fmt.Sprintf("https://example.com/hero/%d", i)
		result.Results = append(result.Results, WebSearchHit{URL: url, Title: fmt.Sprintf("英雄攻略 %d", i), Snippet: fmt.Sprintf("细节%d：", i) + strings.Repeat("技能冷却与被动加成说明。", 90)})
		result.Sources = append(result.Sources, url)
		result.Queries = append(result.Queries, webSearchQueryCandidate{})
		result.Providers = append(result.Providers, webSearchProviderState{})
		result.Attempts = append(result.Attempts, webSearchAttempt{Provider: "searxng", Type: "web", QueryHash: strings.Repeat("h", 40), Status: "ok"})
	}
	raw, err := (&WebSearchTool{maxBytes: DefaultMaxToolOutputChars}).formatExplorationResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(raw)); n > DefaultMaxToolOutputChars {
		t.Fatalf("output runes = %d, want <= %d", n, DefaultMaxToolOutputChars)
	}
	var got webSearchResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.Sources != nil || got.Providers != nil || got.Attempts != nil || got.Queries != nil {
		t.Fatalf("metadata kept while snippets were cut: %s", raw)
	}
	if len(got.Results) != 8 {
		t.Fatalf("hits = %d, want all 8", len(got.Results))
	}
	for _, hit := range got.Results {
		if n := len([]rune(hit.Snippet)); n < searchSnippetFloor {
			t.Fatalf("snippet shrank to %d runes, want >= %d: %q", n, searchSnippetFloor, hit.Snippet)
		}
	}
}
