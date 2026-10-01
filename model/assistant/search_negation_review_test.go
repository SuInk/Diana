// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestParseSearchNegationReview(t *testing.T) {
	if claim, ok := parseSearchNegationReview("```json\n{\"unsupported\":true,\"claim\":\"秋促 11 月下旬才开\"}\n```"); !ok || claim != "秋促 11 月下旬才开" {
		t.Fatalf("claim=%q ok=%v", claim, ok)
	}
	// 自定义过输出格式的配置还在用只复核否定句时的字段名。
	if claim, ok := parseSearchNegationReview(`{"unsupported_negation":true,"claim":"6.1 没发布"}`); !ok || claim != "6.1 没发布" {
		t.Fatalf("旧字段名没认: claim=%q ok=%v", claim, ok)
	}
	for _, raw := range []string{`{"unsupported":false,"claim":"x"}`, `看不懂`, `{"unsupported":"yes"}`} {
		if _, ok := parseSearchNegationReview(raw); ok {
			t.Errorf("%q 不该被当成打回", raw)
		}
	}
	if claim, ok := parseSearchNegationReview(`{"unsupported":true}`); !ok || claim == "" {
		t.Fatalf("缺 claim 时要有兜底说法: %q", claim)
	}
}

// web_search 的输出开头一千多字是查询计划，按字数截断会把结果全截掉。
func TestSearchNegationEvidenceTakesResultsNotMetadata(t *testing.T) {
	search, _ := json.Marshal(map[string]any{
		"source_notice": strings.Repeat("说明", 800),
		"queries":       []map[string]any{{"query": "steam autumn sale"}},
		"status":        "ok",
		"sources":       []string{"https://store.example/news"},
		"content":       "Autumn Sale runs from October 1 to October 8",
	})
	page, _ := json.Marshal(map[string]any{
		"retrieved_at":     "2026-09-30T16:30:08Z",
		"url":              "https://store.example/news",
		"title":            "Sale schedule",
		"text":             "Next Fest: October 12",
		"navigation_chain": []string{strings.Repeat("x", 3000)},
	})
	evidence := searchNegationEvidenceFromSteps([]agent.Step{
		{Tool: agent.WebSearchToolName, Output: string(search)},
		{Tool: "browser_render", Output: string(page)},
	})
	if len(evidence) != 2 {
		t.Fatalf("evidence=%+v", evidence)
	}
	if got := evidence[0].Output; !strings.Contains(got, "October 1 to October 8") || !strings.Contains(got, "https://store.example/news") || strings.Contains(got, "说明") {
		t.Fatalf("检索结果没摘对: %q", got)
	}
	if got := evidence[1].Output; !strings.Contains(got, "Next Fest: October 12") || !strings.Contains(got, "Sale schedule") || strings.Contains(got, "xxx") {
		t.Fatalf("网页正文没摘对: %q", got)
	}
	// 解析不了的输出原样给，别让复核什么都看不到。
	if got := searchNegationEvidenceFromSteps([]agent.Step{{Tool: "browser_render", Output: "plain text"}}); got[0].Output != "plain text" {
		t.Fatalf("非 JSON 输出被吞了: %q", got[0].Output)
	}
}

// browser_render 按 find 找到的段落排在正文前面：正文长时按字数截断也不会丢。
func TestSearchNegationEvidencePutsFindMatchesFirst(t *testing.T) {
	page, _ := json.Marshal(map[string]any{
		"url":          "https://dimagent.example/docs/credits",
		"title":        "套餐与 Credits",
		"text":         strings.Repeat("导航", 3000),
		"find_matches": []string{"Nano 套餐 ¥9.9 / 月"},
	})
	evidence := searchNegationEvidenceFromSteps([]agent.Step{{Tool: "browser_render", Output: string(page)}})
	if got := evidence[0].Output; !strings.Contains(got, "页内查找命中：Nano 套餐 ¥9.9 / 月") {
		t.Fatalf("find 命中被截掉了: %q", got[:200])
	}
}

// 检索记录超出总量时保留最后几次：它们最接近草稿的依据。
func TestSearchNegationEvidenceKeepsLatestWithinBudget(t *testing.T) {
	long := strings.Repeat("字", searchNegationReviewStepRunes*2)
	var steps []agent.Step
	for i := 0; i < 10; i++ {
		steps = append(steps, agent.Step{Tool: "web_search", Input: map[string]any{"query": i}, Output: long})
	}
	evidence := searchNegationEvidenceFromSteps(steps)
	total := 0
	for _, item := range evidence {
		total += len([]rune(item.Output))
	}
	if total > searchNegationReviewTotalRunes+len(evidence) {
		t.Fatalf("超出总量: %d", total)
	}
	if last := evidence[len(evidence)-1]; !strings.Contains(last.Input, "9") {
		t.Fatalf("最后一次检索没保留: %+v", last.Input)
	}
}
