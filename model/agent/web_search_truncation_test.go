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
	// 第一页带诊断，后面的页只带命中。
	if len(got.Sources) != 8 || len(got.Providers) != 8 || len(got.Attempts) != 8 || len(got.Queries) != 8 {
		t.Fatalf("first page metadata: %s", raw)
	}
	if len(got.Results) == 0 || len(got.Results) == 8 || got.NextOffset == 0 || got.TotalResults != 8 {
		t.Fatalf("hits = %d next = %d", len(got.Results), got.NextOffset)
	}
	second, err := webSearchPage(result, map[string]any{"query": "英雄"}, got.NextOffset, 8_000)
	if err != nil {
		t.Fatal(err)
	}
	var next webSearchResult
	if err := json.Unmarshal([]byte(second), &next); err != nil {
		t.Fatal(err)
	}
	if next.Providers != nil || next.Attempts != nil || next.Queries != nil || next.Sources != nil || len(next.Results) == 0 {
		t.Fatalf("later pages should carry hits only: %.300s", second)
	}
	for i, hit := range got.Results {
		if hit.Snippet != result.Results[i].Snippet {
			t.Fatalf("snippet %d was shortened", i)
		}
	}
}

// 一条命中的摘要比整页还长时从摘要中间切开，续读落在这条命中内部，拼起来不丢字。
func TestWebSearchPageSplitsOversizedHit(t *testing.T) {
	long := strings.Repeat("很长的摘要内容。", 2000)
	result := finishExplorationResult(webSearchResult{Status: "ok", Results: []WebSearchHit{{URL: "https://example.com/a", Title: "A", Snippet: "短"}, {URL: "https://example.com/b", Title: "B", Snippet: long}, {URL: "https://example.com/c", Title: "C", Snippet: "尾"}}})
	var snippetB strings.Builder
	var urls []string
	for offset, calls := 0, 0; ; calls++ {
		if calls > 50 {
			t.Fatal("pagination did not finish")
		}
		raw, err := webSearchPage(result, map[string]any{"query": "q"}, offset, 2_000)
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(raw)); n > 2_000 {
			t.Fatalf("page has %d runes", n)
		}
		var got webSearchResult
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		for _, hit := range got.Results {
			if len(urls) == 0 || urls[len(urls)-1] != hit.URL {
				urls = append(urls, hit.URL)
			}
			if hit.URL == "https://example.com/b" {
				if hit.SnippetOffset != len([]rune(snippetB.String())) || hit.SnippetChars != len([]rune(long)) {
					t.Fatalf("snippet_offset = %d after %d runes", hit.SnippetOffset, len([]rune(snippetB.String())))
				}
				snippetB.WriteString(hit.Snippet)
			}
		}
		if got.NextOffset == 0 {
			break
		}
		offset = got.NextOffset
	}
	if snippetB.String() != long || strings.Join(urls, " ") != "https://example.com/a https://example.com/b https://example.com/c" {
		t.Fatalf("hits = %v, snippet %d of %d runes", urls, len([]rune(snippetB.String())), len([]rune(long)))
	}
}
