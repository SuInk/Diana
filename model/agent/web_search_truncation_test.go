// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 装不下时不再先丢 sources、providers、attempts、queries，也不把摘要砍短：诊断字段每页
// 都在，命中整条留到下一页。
func TestWebSearchPageKeepsMetadataAndWholeSnippets(t *testing.T) {
	result := webSearchResult{Status: "ok", Strategy: "parallel_queries", Budget: webSearchBudget{MaxQueries: 4, QueriesUsed: 4}}
	for i := 0; i < 8; i++ {
		url := fmt.Sprintf("https://example.com/hero/%d", i)
		result.Results = append(result.Results, WebSearchHit{URL: url, Title: fmt.Sprintf("英雄攻略 %d", i), Snippet: fmt.Sprintf("细节%d：", i) + strings.Repeat("技能冷却与被动加成说明。", 90)})
		result.Sources = append(result.Sources, url)
		result.Queries = append(result.Queries, webSearchQueryCandidate{})
		result.Providers = append(result.Providers, webSearchProviderState{})
		result.Attempts = append(result.Attempts, webSearchAttempt{Provider: "searxng", Type: "web", QueryHash: strings.Repeat("h", 40), Status: "ok"})
	}
	raw, err := webSearchPage(result, map[string]any{"query": "英雄"}, 0, 8_000)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(raw)); n > 8_000 {
		t.Fatalf("output runes = %d, want <= 8000", n)
	}
	var got webSearchResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 8 || len(got.Providers) != 8 || len(got.Attempts) != 8 || len(got.Queries) != 8 {
		t.Fatalf("metadata dropped: %s", raw)
	}
	if len(got.Results) == 0 || len(got.Results) == 8 || got.NextOffset != len(got.Results) || got.TotalResults != 8 {
		t.Fatalf("hits = %d next = %d", len(got.Results), got.NextOffset)
	}
	for i, hit := range got.Results {
		if hit.Snippet != result.Results[i].Snippet {
			t.Fatalf("snippet %d was shortened", i)
		}
	}
}
