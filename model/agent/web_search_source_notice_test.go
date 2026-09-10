package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchResultIncludesSourceNoticeWithoutChangingContent(t *testing.T) {
	content := "Published: 2026-09-03\n转载内容"
	raw, err := (&WebSearchTool{maxBytes: 4000}).formatExplorationResult(webSearchResult{Status: "ok", Sources: []string{"https://example.com/article"}, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Content != content {
		t.Fatal("notice must not rewrite source content or dates")
	}
	for _, want := range []string{"可能来自转载", "不一定", "真实发布时间", "未核实原始来源", "最新"} {
		if !strings.Contains(result.SourceNotice, want) {
			t.Errorf("source notice missing %q", want)
		}
	}
}

func TestSearchSourceNoticeSurvivesContentTruncation(t *testing.T) {
	raw, err := (&WebSearchTool{maxBytes: 1000}).formatExplorationResult(webSearchResult{Status: "ok", Content: strings.Repeat("长搜索内容", 1000)})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.SourceNotice == "" || len([]rune(raw)) > 1000 {
		t.Fatal("notice lost or output budget exceeded")
	}
}
