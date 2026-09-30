// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestParseSearchNegationReview(t *testing.T) {
	if claim, ok := parseSearchNegationReview("```json\n{\"unsupported_negation\":true,\"claim\":\"6.1 没发布\"}\n```"); !ok || claim != "6.1 没发布" {
		t.Fatalf("claim=%q ok=%v", claim, ok)
	}
	for _, raw := range []string{`{"unsupported_negation":false,"claim":"x"}`, `看不懂`, `{"unsupported_negation":"yes"}`} {
		if _, ok := parseSearchNegationReview(raw); ok {
			t.Errorf("%q 不该被当成打回", raw)
		}
	}
	if claim, ok := parseSearchNegationReview(`{"unsupported_negation":true}`); !ok || claim == "" {
		t.Fatalf("缺 claim 时要有兜底说法: %q", claim)
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
