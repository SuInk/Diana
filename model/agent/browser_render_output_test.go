// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
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
	page, err := parseRenderedPage([]byte(html), "https://dimagent.example/en/", false)
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

// find 返回命中所在的整行连同前后一行，命中 find 的链接排最前。
func TestBrowserRenderFindReturnsWholeLines(t *testing.T) {
	out := runBrowserRenderForTest(t, browserRenderTestHTML, "套餐|pricing")
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

func TestBrowserRenderFindMatchesMergeAdjacentLines(t *testing.T) {
	text := "第一行\n价格A 在这\n中间一行\n价格B 在这\n" + strings.Repeat("无关\n", 5) + "价格C\n结尾"
	matches := browserRenderFindMatches(text, []string{"价格"})
	if len(matches) != 2 || matches[0].excerpt != "第一行\n价格A 在这\n中间一行\n价格B 在这\n无关" || !strings.Contains(matches[1].excerpt, "价格C") {
		t.Fatalf("matches = %#v", matches)
	}
	if matches[0].start != 0 || !strings.HasPrefix(string([]rune(text)[matches[1].start:]), "无关\n价格C") {
		t.Fatalf("starts = %#v", matches)
	}
	var many strings.Builder
	for i := 0; i < 20; i++ {
		many.WriteString("Price " + strings.Repeat("-", 500) + "\n" + strings.Repeat("=\n", 3))
	}
	if got := browserRenderFindMatches(many.String(), []string{"price"}); len(got) != 20 {
		t.Fatalf("every match must be returned, got %d", len(got))
	}
}

// 定价表这类一行挨一行都命中的页面不能并成一整段；单行超长时只取命中附近，完整
// 内容照 find_offsets 读。每段都不超过上限。
func TestBrowserRenderFindMatchesStayBounded(t *testing.T) {
	var table strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&table, "| 套餐 %d | 价格 ¥%d / 月 |\n", i, i*10)
	}
	long := strings.Repeat("前文", 3000) + "价格在这里" + strings.Repeat("后文", 3000)
	for _, text := range []string{table.String(), long} {
		matches := browserRenderFindMatches(text, []string{"价格"})
		if len(matches) == 0 {
			t.Fatal("no matches")
		}
		for _, match := range matches {
			if n := len([]rune(match.excerpt)); n > browserRenderFindMatchChars {
				t.Fatalf("match has %d runes", n)
			}
			if !strings.Contains(match.excerpt, "价格") {
				t.Fatalf("excerpt lost the hit: %q", match.excerpt)
			}
		}
	}
	raw, err := browserRenderOutput(RenderedPage{URL: "https://example.com", Text: long}, "价格", 0, 4000)
	if err != nil || len([]rune(raw)) > 4000 {
		t.Fatalf("find page over budget: %d %v", len([]rune(raw)), err)
	}
}

// 跳转前页面的正文和超长链接也按字分页：单条比整页还长时从中间切开，续读落在
// 条目内部，拼起来不丢字。
func TestBrowserRenderSplitsOversizedPreviousPageAndLink(t *testing.T) {
	previous := strings.Repeat("跳转前的正文", 2000)
	link := strings.Repeat("超长链接文字", 600)
	page := RenderedPage{URL: "https://example.com/final", RequestedURL: "https://example.com/start", Text: "最终页正文",
		Links:         []RenderedLink{{URL: "https://example.com/a", Text: link}},
		PreviousPages: []RenderedPageSnapshot{{URL: "https://example.com/start", Title: "起始页", Text: previous}}}
	var gotPrevious, gotLinks strings.Builder
	for offset, calls := 0, 0; ; calls++ {
		if calls > 100 {
			t.Fatal("pagination did not finish")
		}
		raw, err := browserRenderOutput(page, "", offset, 2000)
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(raw)); n > 2000 {
			t.Fatalf("page has %d runes", n)
		}
		var out browserRenderPayload
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range out.PreviousPages {
			gotPrevious.WriteString(snapshot.Text)
		}
		for _, line := range out.Links {
			gotLinks.WriteString(line)
		}
		if out.NextOffset == 0 {
			break
		}
		offset = out.NextOffset
	}
	if gotPrevious.String() != previous {
		t.Fatalf("previous page text: got %d runes, want %d", len([]rune(gotPrevious.String())), len([]rune(previous)))
	}
	if gotLinks.String() != link+" | https://example.com/a" {
		t.Fatalf("link pages do not add up: %d runes", len([]rune(gotLinks.String())))
	}
}

// 照 Pi 的做法分页：不缩短正文、不丢链接，按整行装进上限，照抄 next_offset 能把整页读完。
func TestBrowserRenderPagesWholeLinesUntilEverythingIsRead(t *testing.T) {
	var text strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&text, "第 %03d 行正文，内容不能被截掉。\n", i)
	}
	page := RenderedPage{URL: "https://example.com/docs", RequestedURL: "https://example.com/docs", Text: strings.TrimSpace(text.String())}
	for i := 0; i < 120; i++ {
		page.Links = append(page.Links, RenderedLink{URL: fmt.Sprintf("https://example.com/p/%d", i), Text: strings.Repeat("链接文字", 20)})
	}
	for i := 0; i < 30; i++ {
		page.Images = append(page.Images, RenderedImage{URL: fmt.Sprintf("https://example.com/i/%d.png", i), Alt: strings.Repeat("图", 200)})
	}
	page.PreviousPages = []RenderedPageSnapshot{{URL: "https://example.com/a", Text: strings.Repeat("前", 2000)}, {URL: "https://example.com/b"}, {URL: "https://example.com/c"}, {URL: "https://example.com/d"}}
	const budget = 3000
	var gotText []string
	var links, images, calls int
	previousSeen := map[string]bool{}
	for offset := 0; ; calls++ {
		if calls > 200 {
			t.Fatal("pagination did not finish")
		}
		raw, err := browserRenderOutput(page, "", offset, budget)
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(raw)); n > budget {
			t.Fatalf("page %d has %d runes, budget %d", calls, n, budget)
		}
		var out browserRenderPayload
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		if out.Text != "" {
			gotText = append(gotText, out.Text)
		}
		links += len(out.Links)
		images += len(out.Images)
		for _, snapshot := range out.PreviousPages {
			previousSeen[snapshot.URL] = true
		}
		if out.NextOffset == 0 {
			if out.ReadNotice != "" || out.Truncated {
				t.Fatalf("last page still says it is partial: %s", raw)
			}
			break
		}
		if out.NextOffset <= offset || !strings.Contains(out.ReadNotice, fmt.Sprintf(`"offset":%d`, out.NextOffset)) {
			t.Fatalf("notice does not say how to continue: %s", raw)
		}
		offset = out.NextOffset
	}
	if strings.Join(gotText, "\n") != page.Text {
		t.Fatal("text pages do not add up to the full text")
	}
	if links != 120 || images != 30 || len(previousSeen) != 4 {
		t.Fatalf("links=%d images=%d previous=%d", links, images, len(previousSeen))
	}
	for _, chunk := range gotText {
		if !strings.HasPrefix(chunk, "第 ") || !strings.HasSuffix(chunk, "。") {
			t.Fatalf("text page was cut inside a line: %q", chunk)
		}
	}
}

// 一行比整页上限还长时从行内切，下一页从切口接上，不丢字。
func TestBrowserRenderSplitsOverlongLineWithoutLosingText(t *testing.T) {
	page := RenderedPage{URL: "https://example.com/one-line", Text: strings.Repeat("长", 5000) + "结尾"}
	var got strings.Builder
	for offset, calls := 0, 0; ; calls++ {
		if calls > 50 {
			t.Fatal("pagination did not finish")
		}
		raw, err := browserRenderOutput(page, "", offset, 1500)
		if err != nil {
			t.Fatal(err)
		}
		var out browserRenderPayload
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		got.WriteString(out.Text)
		if out.NextOffset == 0 {
			break
		}
		offset = out.NextOffset
	}
	if got.String() != page.Text {
		t.Fatalf("got %d runes, want %d", len([]rune(got.String())), len([]rune(page.Text)))
	}
}

// 带 find 时命中排在最前，find_offsets 填进 offset 就能从那段正文读起。
func TestBrowserRenderFindOffsetsJumpIntoText(t *testing.T) {
	page := RenderedPage{URL: "https://docs.example/architecture", Text: strings.Repeat("正文正文正文\n", 3000) + "sandbox execution 有条件支持\n后文"}
	raw, err := browserRenderOutput(page, "sandbox", 0, 1500)
	if err != nil {
		t.Fatal(err)
	}
	var out browserRenderPayload
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.FindMatches) != 1 || !strings.Contains(out.FindMatches[0], "有条件支持") || len(out.FindOffsets) != 1 || !out.Truncated || out.NextOffset == 0 {
		t.Fatalf("find page = %s", raw)
	}
	jumped, err := browserRenderOutput(page, "sandbox", out.FindOffsets[0], 1500)
	if err != nil {
		t.Fatal(err)
	}
	var there browserRenderPayload
	if err := json.Unmarshal([]byte(jumped), &there); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(there.Text, "sandbox execution 有条件支持") {
		t.Fatalf("offset from find_offsets does not land on the passage: %q", there.Text)
	}
}

// 批量读取：短页整页给，剩下的给长页，不再按页数平分。
func TestBrowserRenderBatchSharesGiveShortPagesWholeOutput(t *testing.T) {
	short := newBrowserRenderSequence(RenderedPage{URL: "https://a.example", Text: "短页"}, "")
	long := newBrowserRenderSequence(RenderedPage{URL: "https://b.example", Text: strings.Repeat("长页正文\n", 4000)}, "")
	shares, err := browserRenderBatchShares([]browserRenderSequence{short, long}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := short.render(0, math.MaxInt)
	if shares[0] != len([]rune(full)) || shares[0]+shares[1] != 10000 {
		t.Fatalf("shares = %v", shares)
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

func TestParseRenderedPageListsDownloadableImages(t *testing.T) {
	html := `<html><head><title>SPY</title><meta property="og:image" content="/og.png"></head><body><main>
<p>S&amp;P 500 daily chart</p>
<img src="/pixel.gif" width="1" height="1">
<img src="/icon.svg" alt="logo">
<img src="placeholder.gif" data-src="https://charts.example.com/chart.png?t=SPY" alt="SPY daily">
<img src="/og.png">
<div hidden><img src="/hidden.png"></div>
</main></body></html>`
	page, err := parseRenderedPage([]byte(html), "https://example.com/quote/SPY", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []RenderedImage{{URL: "https://example.com/og.png"}, {URL: "https://charts.example.com/chart.png?t=SPY", Alt: "SPY daily"}}
	if !reflect.DeepEqual(page.Images, want) {
		t.Fatalf("images = %#v", page.Images)
	}
	out, err := browserRenderOutput(page, "", 0, ToolOutputMaxChars)
	if err != nil || !strings.Contains(out, `"images"`) {
		t.Fatalf("browser_render output should carry images: %v %s", err, out)
	}
}
