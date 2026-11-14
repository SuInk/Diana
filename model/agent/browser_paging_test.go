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
