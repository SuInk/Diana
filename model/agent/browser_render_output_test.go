// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type browserRenderOutputForTest struct {
	Text        string   `json:"text"`
	Links       []string `json:"links"`
	FindMatches []string `json:"find_matches"`
	FindNote    string   `json:"find_note"`
}

func runBrowserRenderForTest(t *testing.T, html, find string) browserRenderOutputForTest {
	t.Helper()
	page, err := parseRenderedPage([]byte(html), "https://dimagent.example/en/", 40, false)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewBrowserRenderTool(PageRendererFunc(func(context.Context, string) (RenderedPage, error) { return page, nil }))
	input := map[string]any{"url": "https://dimagent.example/en/"}
	if find != "" {
		input["find"] = find
	}
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var out browserRenderOutputForTest
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const browserRenderTestHTML = `<html><head><title>DimAgent</title></head><body>
<nav><a href="#top">Top</a><a href="https://twitter.example/dim">Twitter</a><a href="/docs/">Docs</a><a href="/docs/">文档</a><a href="https://docs.dimagent.example/credits">套餐与 Credits</a><a href="javascript:void(0)">Menu</a></nav>
<main><p>One agent. Everywhere. An agent runtime for desktop, terminal, scripts, and editors.</p>
<p>很长的介绍很长的介绍很长的介绍很长的介绍很长的介绍很长的介绍很长的介绍很长的介绍。</p>
<p>Nano 套餐 ¥9.9 / 月，含 1,500 Credits。Pro 套餐 ¥299 / 月。</p></main></body></html>`

// 页面链接交给模型：去掉页内锚点、javascript 和重复地址，同站的排在外站前面。
func TestBrowserRenderListsPageLinks(t *testing.T) {
	out := runBrowserRenderForTest(t, browserRenderTestHTML, "")
	joined := strings.Join(out.Links, "\n")
	if strings.Contains(joined, "#top") || strings.Contains(joined, "javascript") {
		t.Fatalf("anchor or javascript link kept: %v", out.Links)
	}
	if strings.Count(joined, "https://dimagent.example/docs/") != 1 {
		t.Fatalf("duplicate link kept: %v", out.Links)
	}
	if len(out.Links) != 3 || !strings.HasPrefix(out.Links[0], "Docs | https://dimagent.example/docs/") || !strings.Contains(out.Links[2], "twitter.example") {
		t.Fatalf("links = %v", out.Links)
	}
}

// find 在截断前的整页正文里找，返回命中附近的段落；命中 find 的链接排最前。
func TestBrowserRenderFindsTermsBeyondTruncatedText(t *testing.T) {
	out := runBrowserRenderForTest(t, browserRenderTestHTML, "套餐|pricing")
	if strings.Contains(out.Text, "¥9.9") {
		t.Fatalf("text should be truncated for this test: %q", out.Text)
	}
	if len(out.FindMatches) != 1 || !strings.Contains(out.FindMatches[0], "¥9.9") || !strings.Contains(out.FindMatches[0], "¥299") {
		t.Fatalf("matches = %q", out.FindMatches)
	}
	if !strings.HasPrefix(out.Links[0], "套餐与 Credits | ") {
		t.Fatalf("matching link should come first: %v", out.Links)
	}

	missing := runBrowserRenderForTest(t, browserRenderTestHTML, "退款")
	if len(missing.FindMatches) != 0 || !strings.Contains(missing.FindNote, "没找到 退款") || !strings.Contains(missing.FindNote, "不等于没有") {
		t.Fatalf("missing = %#v", missing)
	}
}

func TestBrowserRenderFindMatchesMergeAndCap(t *testing.T) {
	text := strings.Repeat("x", 1000) + "价格A" + strings.Repeat("y", 50) + "价格B" + strings.Repeat("z", 1000)
	matches := browserRenderFindMatches(text, []string{"价格"})
	if len(matches) != 1 || !strings.Contains(matches[0], "价格A") || !strings.Contains(matches[0], "价格B") {
		t.Fatalf("nearby hits should merge: %q", matches)
	}
	if !strings.HasPrefix(matches[0], "…") || !strings.HasSuffix(matches[0], "…") {
		t.Fatalf("excerpt should mark both cut ends: %q", matches[0])
	}
	var many strings.Builder
	for i := 0; i < 20; i++ {
		many.WriteString("Price " + strings.Repeat("-", 500))
	}
	if got := browserRenderFindMatches(many.String(), []string{"price"}); len(got) != browserRenderMaxMatches {
		t.Fatalf("matches = %d, want %d", len(got), browserRenderMaxMatches)
	}
}

// Runner 不能在 JSON 中间截断，否则模型读不到 text/find_matches。
func TestBrowserRenderKeepsValidJSONWithinRunnerBudget(t *testing.T) {
	page := RenderedPage{URL: "https://example.com/docs", Text: strings.Repeat("正文", 5000), FullText: strings.Repeat("正文", 5000) + "关键能力有条件支持"}
	for i := 0; i < 40; i++ {
		page.Links = append(page.Links, RenderedLink{URL: "https://example.com/" + strings.Repeat("a", i+100), Text: "文档"})
	}
	raw, err := browserRenderOutputWithBudget(page, "关键能力", 1500)
	if err != nil {
		t.Fatal(err)
	}
	var out browserRenderOutputForTest
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len([]rune(raw)) > 1500 || !strings.Contains(strings.Join(out.FindMatches, " "), "关键能力有条件支持") {
		t.Fatalf("budget or evidence lost: %s", raw)
	}
}

func TestBrowserRenderMissingURLExplainsNativeArguments(t *testing.T) {
	tool := NewBrowserRenderTool(PageRendererFunc(func(context.Context, string) (RenderedPage, error) {
		t.Fatal("missing URL reached renderer")
		return RenderedPage{}, nil
	}))
	_, err := tool.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), `arguments 形如 {"url"`) || !strings.Contains(err.Error(), "不要只发工具名") {
		t.Fatalf("missing native arguments not explained: %v", err)
	}
}

func TestBrowserRenderTruncationProvidesFindRecovery(t *testing.T) {
	page := RenderedPage{URL: "https://docs.example/architecture", Text: strings.Repeat("正文", 5000), FullText: strings.Repeat("正文", 5000) + "sandbox execution", Truncated: true}
	raw, err := browserRenderOutputWithBudget(page, "", 1500)
	if err != nil {
		t.Fatal(err)
	}
	var out browserRenderPayload
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len([]rune(raw)) > 1500 || !strings.Contains(out.ReadNotice, `"find"`) {
		t.Fatalf("truncated source lacks usable recovery: %s", raw)
	}
	found, err := browserRenderOutputWithBudget(page, "sandbox", 1500)
	if err != nil || !strings.Contains(found, "sandbox execution") {
		t.Fatalf("find cannot recover omitted passage: %s %v", found, err)
	}
}
