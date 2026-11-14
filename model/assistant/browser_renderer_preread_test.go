// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 预读进上下文的是第一页：正文和跳转前页面保持原来的量级，后面的写明用 browser_render
// 带 offset 续读。
func TestRenderedPageContextGivesFirstPageAndContinuation(t *testing.T) {
	var text strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&text, "第 %d 行正文\n", i)
	}
	page := agent.RenderedPage{RequestedURL: "https://example.com/doc", URL: "https://example.com/doc", Title: "文档", Text: text.String(), Stable: true}
	for i := 0; i < 5; i++ {
		page.PreviousPages = append(page.PreviousPages, agent.RenderedPageSnapshot{URL: fmt.Sprintf("https://example.com/p%d", i), Text: strings.Repeat("跳转前", 1000)})
	}
	context := renderedPageContext(page)
	first, next := agent.FirstTextPage(page.Text, browserPrereadTextChars)
	lines := strings.Split(first, "\n")
	if !strings.Contains(context, lines[0]) || !strings.Contains(context, lines[len(lines)-1]) || strings.Contains(context, "第 1999 行正文") {
		t.Fatal("context should carry exactly the first page of the text")
	}
	if !strings.Contains(context, fmt.Sprintf(`"offset":%d`, next)) || !strings.Contains(context, "browser_render") {
		t.Fatalf("continuation missing: %s", context[len(context)-400:])
	}
	if strings.Contains(context, "https://example.com/p3") || !strings.Contains(context, "还有 2 个没列出") {
		t.Fatal("previous pages should list the first three and say how many are left")
	}
	if n := len([]rune(context)); n > browserPrereadTextChars+browserPrereadPreviousPages*(browserPrereadPreviousChars+300)+2000 {
		t.Fatalf("preread context grew to %d runes", n)
	}
}
