// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// browser_text 按整行分页：照抄 next_offset 能把整页文字读完，每页不超过上限。
func TestBrowserTextPagesWholePage(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 5*time.Second)
	if _, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/"}); err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&want, "第 %d 行 <页面文字>\n", i)
	}
	f.mu.Lock()
	f.evalValue = map[string]any{"url": "https://example.com/", "title": "长页", "text": want.String()}
	f.mu.Unlock()
	ctx := WithToolOutputBudget(context.Background(), 1500)
	var got strings.Builder
	for offset, calls := 0, 0; ; calls++ {
		if calls > 50 {
			t.Fatal("pagination did not finish")
		}
		out, err := runBrowserTool(ctx, registry, "browser_text", map[string]any{"offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(out)); n > 1500 {
			t.Fatalf("page has %d runes", n)
		}
		var page browserPageSnapshot
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		got.WriteString(page.Text)
		if page.NextOffset == 0 {
			break
		}
		if !strings.HasSuffix(page.Text, "\n") || !strings.Contains(page.ReadNotice, fmt.Sprintf(`"offset":%d`, page.NextOffset)) {
			t.Fatalf("page not cut on a line or notice missing: %s", out)
		}
		offset = page.NextOffset
	}
	if got.String() != want.String() {
		t.Fatalf("pages add up to %d runes, want %d", len([]rune(got.String())), len([]rune(want.String())))
	}
}

// browser_eval 的长结果留在缓存里按 result_id 续读，不重跑脚本，拼起来就是原结果。
func TestBrowserEvalPagesLongResultWithoutRerunning(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 5*time.Second)
	if _, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/"}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("结果数据", 1000)
	f.mu.Lock()
	f.evalValue = long
	f.mu.Unlock()
	ctx := WithToolOutputBudget(context.Background(), 1500)
	out, err := runBrowserTool(ctx, registry, "browser_eval", map[string]any{"script": "data"})
	if err != nil {
		t.Fatal(err)
	}
	var page browserEvalPage
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatal(err)
	}
	got := page.Result
	f.mu.Lock()
	f.evalValue = "脚本被重跑了"
	f.mu.Unlock()
	for calls := 0; page.NextOffset != 0; calls++ {
		if calls > 50 || len([]rune(out)) > 1500 {
			t.Fatalf("bad page: %d runes", len([]rune(out)))
		}
		out, err = runBrowserTool(ctx, registry, "browser_eval", map[string]any{"result_id": page.ResultID, "offset": page.NextOffset})
		if err != nil {
			t.Fatal(err)
		}
		page = browserEvalPage{}
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		got += page.Result
	}
	if !strings.Contains(got, long) || strings.Contains(got, "重跑") {
		t.Fatalf("pages add up to %d runes", len([]rune(got)))
	}
	if _, err := runBrowserTool(ctx, registry, "browser_eval", map[string]any{"result_id": "eval-999"}); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("unknown result_id err = %v", err)
	}
}

// 交给模型的搜索结果去掉诊断字段后仍是紧凑 JSON，分页字段排在最前面。
func TestWebSearchOutputForModelKeepsPagingFirst(t *testing.T) {
	raw, err := webSearchPage(finishExplorationResult(webSearchResult{Status: "ok", Strategy: "s", Providers: []webSearchProviderState{{Provider: "p"}}, Results: []WebSearchHit{{URL: "https://example.com/a", Snippet: strings.Repeat("摘要", 400)}, {URL: "https://example.com/b", Snippet: strings.Repeat("摘要", 400)}}}), map[string]any{"query": "q"}, 0, 1500)
	if err != nil {
		t.Fatal(err)
	}
	slim := webSearchOutputForModel(raw)
	if !strings.HasPrefix(slim, `{"offset"`) && !strings.HasPrefix(slim, `{"total_results"`) {
		t.Fatalf("paging fields not first: %.120s", slim)
	}
	if strings.Contains(slim, `"providers"`) || strings.Contains(slim, "\n  ") || len([]rune(slim)) > len([]rune(raw)) || !strings.Contains(slim, `"next_offset"`) {
		t.Fatalf("slim output = %.300s", slim)
	}
}

// 单页读取在调用方的期限里读，读完记进缓存：续读不重新渲染。
func TestBrowserRenderContinuationReusesThePage(t *testing.T) {
	var renders int
	text := strings.Repeat("一行正文内容\n", 5000)
	tool := NewBrowserRenderTool(PageRendererFunc(func(_ context.Context, raw string) (RenderedPage, error) {
		renders++
		return RenderedPage{RequestedURL: raw, URL: raw, Text: text}, nil
	}))
	ctx := WithToolOutputBudget(context.Background(), 3000)
	first, err := tool.Run(ctx, map[string]any{"url": "https://example.com/doc"})
	if err != nil {
		t.Fatal(err)
	}
	var page browserRenderPayload
	if err := json.Unmarshal([]byte(first), &page); err != nil || page.NextOffset == 0 {
		t.Fatalf("first page: %v %.200s", err, first)
	}
	if _, err := tool.Run(ctx, map[string]any{"url": "https://example.com/doc", "offset": page.NextOffset}); err != nil {
		t.Fatal(err)
	}
	if renders != 1 {
		t.Fatalf("renders = %d, want 1", renders)
	}
}

// 没找到选项时候选整条分页，照 next_options_offset 能看完。
func TestBrowserSelectMissPagesEveryOption(t *testing.T) {
	var options []map[string]string
	for i := 0; i < 300; i++ {
		options = append(options, map[string]string{"value": fmt.Sprintf("v%d", i), "label": fmt.Sprintf("选项 %d", i)})
	}
	raw, _ := json.Marshal(map[string]any{"ok": false, "error": "option not found", "options": options})
	seen := 0
	for offset, calls := 0, 0; ; calls++ {
		if calls > 50 {
			t.Fatal("pagination did not finish")
		}
		out, ok := browserSelectMissPage(raw, offset, 1500)
		if !ok || len([]rune(out)) > 1500 {
			t.Fatalf("bad page: %v %d", ok, len([]rune(out)))
		}
		var page browserSelectMiss
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		for _, option := range page.Options {
			if option["value"] != fmt.Sprintf("v%d", seen) {
				t.Fatalf("option %d = %v", seen, option)
			}
			seen++
		}
		if page.NextOptionsOffset == 0 {
			break
		}
		offset = page.NextOptionsOffset
	}
	if seen != 300 {
		t.Fatalf("saw %d options", seen)
	}
	if _, ok := browserSelectMissPage([]byte(`{"ok":true,"value":"v1"}`), 0, 1500); ok {
		t.Fatal("success result should pass through")
	}
}

// 结果页补摘要按结果顺序找标题、以下一条为界；侧栏里提前出现的标题不带偏，最后一条
// 不吞页脚和相关搜索。
func TestFillSearchEngineSnippetsBoundsByNextResult(t *testing.T) {
	text := "侧栏推荐\n第二条标题\n第一条标题\n第一条的摘要\n第二条标题\n第二条的摘要\n第三条标题\n第三条的摘要\n相关搜索\n页脚 版权所有"
	results := []searchEngineResult{{Title: "第一条标题"}, {Title: "第二条标题"}, {Title: "第三条标题"}}
	fillSearchEngineSnippets(text, results)
	want := []string{"第一条的摘要", "第二条的摘要", "第三条的摘要"}
	for i, result := range results {
		if result.Snippet != want[i] {
			t.Fatalf("snippet %d = %q, want %q", i, result.Snippet, want[i])
		}
	}
}
