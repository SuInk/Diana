// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// webSearchModelRunes 是这一页交给模型的字数（去掉诊断字段的精简版）：小页按它计量。
func webSearchModelRunes(raw string) int {
	return utf8.RuneCountInString(webSearchOutputForModel(raw))
}

// 搜索、读网页类工具模型直接调用时用小页；codemode 子调用的大上限和调用方给的更小上限原样保留。
func TestReadingPageBudget(t *testing.T) {
	for _, test := range []struct {
		budget, want int
	}{
		{0, ReadingPageMaxChars},
		{ToolOutputMaxChars, ReadingPageMaxChars},
		{1500, 1500},
		{codeModeChildOutputBudget, codeModeChildOutputBudget},
	} {
		if got := readingPageBudget(WithToolOutputBudget(context.Background(), test.budget)); got != test.want {
			t.Errorf("budget %d: got %d, want %d", test.budget, got, test.want)
		}
	}
}

// 小页按模型看到的精简版计量：第一页的查询、搜索源、尝试记录这些诊断再多，也不挤掉命中；
// 首页写明 next_offset 和还剩几条。
func TestWebSearchSmallPageKeepsHitsDespiteDiagnostics(t *testing.T) {
	result := webSearchResult{Status: "ok", Strategy: "parallel_queries"}
	for i := 0; i < 10; i++ {
		result.Queries = append(result.Queries, webSearchQueryCandidate{Query: strings.Repeat("查询词", 30), Hash: strings.Repeat("h", 40), Status: "done", Reason: strings.Repeat("原因", 60)})
		result.Providers = append(result.Providers, webSearchProviderState{Provider: fmt.Sprintf("provider-%d", i)})
		result.Attempts = append(result.Attempts, webSearchAttempt{Provider: "searxng", Type: "web", QueryHash: strings.Repeat("h", 40), Status: "ok", Error: strings.Repeat("e", 200)})
	}
	for i := 0; i < 30; i++ {
		result.Results = append(result.Results, WebSearchHit{URL: fmt.Sprintf("https://example.com/%d", i), Title: fmt.Sprintf("标题 %d", i), Snippet: strings.Repeat("命中摘要内容。", 40)})
	}
	raw, err := webSearchPage(result, map[string]any{"query": "q"}, 0, ReadingPageMaxChars)
	if err != nil {
		t.Fatal(err)
	}
	if n := webSearchModelRunes(raw); n > ReadingPageMaxChars || len([]rune(raw)) > ToolOutputMaxChars {
		t.Fatalf("model view %d runes, raw %d runes", n, len([]rune(raw)))
	}
	var got webSearchResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 10 {
		t.Fatalf("first page should keep diagnostics for logs: %d attempts", len(got.Attempts))
	}
	if len(got.Results) < 5 || len(got.Results) == 30 || got.NextOffset == 0 {
		t.Fatalf("hits = %d next = %d", len(got.Results), got.NextOffset)
	}
	if !strings.Contains(got.ReadNotice, fmt.Sprintf("命中还有 %d 条（共 30 条）", 30-len(got.Results))) || !strings.Contains(got.ReadNotice, fmt.Sprintf(`"offset":%d`, got.NextOffset)) {
		t.Fatalf("read notice = %s", got.ReadNotice)
	}
	if slim := webSearchOutputForModel(raw); !strings.Contains(slim, fmt.Sprintf(`"next_offset":%d`, got.NextOffset)) {
		t.Fatalf("model view lost next_offset: %.300s", slim)
	}
}

// browser_render 在 Runner 的单次上限下每页只给小页并写明续读；并行读几页时每页各一份小页。
func TestBrowserRenderUsesSmallPages(t *testing.T) {
	text := strings.Repeat("一行比较长的正文内容，用来撑满页面。\n", 2000)
	tool := NewBrowserRenderTool(PageRendererFunc(func(_ context.Context, url string) (RenderedPage, error) {
		return RenderedPage{URL: url, RequestedURL: url, Title: "T", Text: text}, nil
	}))
	ctx := WithToolOutputBudget(context.Background(), ToolOutputMaxChars)
	single, err := tool.Run(ctx, map[string]any{"url": "https://a.example"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(single)); n > ReadingPageMaxChars {
		t.Fatalf("single page has %d runes, want <= %d", n, ReadingPageMaxChars)
	}
	pages := browserRenderPages(single)
	if len(pages) != 1 || pages[0].NextOffset == 0 || !strings.Contains(pages[0].ReadNotice, fmt.Sprintf(`"offset":%d`, pages[0].NextOffset)) {
		t.Fatalf("single page paging = %.400s", single)
	}
	batch, err := tool.Run(ctx, map[string]any{"url": "https://b.example", "urls": []any{"https://c.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(batch)); n > 2*ReadingPageMaxChars {
		t.Fatalf("batch has %d runes, want <= %d", n, 2*ReadingPageMaxChars)
	}
	pages = browserRenderPages(batch)
	if len(pages) != 2 {
		t.Fatalf("batch pages = %d", len(pages))
	}
	for _, page := range pages {
		if page.Text == "" || page.NextOffset == 0 {
			t.Fatalf("batch page should carry a small page and next_offset: %.300s", batch)
		}
	}
}
