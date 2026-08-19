// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type stubSummaryLLMProvider struct {
	reply    string
	requests []llm.GenerateRequest
	err      error
}

// Generate 记录压缩请求并返回预设摘要。
func (p *stubSummaryLLMProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	if p.err != nil {
		return nil, p.err
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.reply}, nil
}

func longContextSummary(header string, lines int) string {
	body := make([]string, 0, lines)
	for index := 0; index < lines; index++ {
		body = append(body, fmt.Sprintf("成员%02d: %s", index, strings.Repeat("这是一句足够长的历史发言", 6)))
	}
	return joinContextSummary(header, body)
}

func TestMergeContextSummaryKeepsWatermarkAndWholeLines(t *testing.T) {
	events := []MessageEvent{
		{Kind: EventKindGroup, UserID: "1", SenderName: "Alice", Time: 1700000000, RawMessage: "第一句"},
		{Kind: EventKindGroup, UserID: "2", SenderName: "Bob", Time: 1700003600, RawMessage: "第二句"},
	}
	summary := mergeContextSummary("", events)

	header, body := splitContextSummary(summary)
	if !strings.HasPrefix(header, contextSummaryHeaderPrefix) {
		t.Fatalf("summary is missing its watermark header: %q", summary)
	}
	if !strings.Contains(header, "共 2 条") {
		t.Fatalf("header does not report the compressed count: %q", header)
	}
	if len(body) != 2 || !strings.Contains(body[0], "Alice") || !strings.Contains(body[1], "Bob") {
		t.Fatalf("summary body = %#v", body)
	}

	// 再压一批，条数累加、起点保持不变、终点前移。
	start, count := parseContextSummaryHeader(header)
	merged := mergeContextSummary(summary, []MessageEvent{
		{Kind: EventKindGroup, UserID: "3", SenderName: "Carol", Time: 1700007200, RawMessage: "第三句"},
	})
	mergedHeader, mergedBody := splitContextSummary(merged)
	mergedStart, mergedCount := parseContextSummaryHeader(mergedHeader)
	if mergedStart != start {
		t.Fatalf("watermark start moved: %q -> %q", start, mergedStart)
	}
	if mergedCount != count+1 {
		t.Fatalf("watermark count = %d, want %d", mergedCount, count+1)
	}
	if len(mergedBody) != 3 {
		t.Fatalf("merged body = %#v", mergedBody)
	}
}

func TestMergeContextSummaryDropsWholeLinesAtTheCap(t *testing.T) {
	events := make([]MessageEvent, 0, 400)
	for index := 0; index < 400; index++ {
		events = append(events, MessageEvent{
			Kind:       EventKindGroup,
			UserID:     fmt.Sprint(index),
			SenderName: fmt.Sprintf("成员%03d", index),
			Time:       1700000000 + int64(index)*60,
			RawMessage: strings.Repeat("一句会被压缩掉的历史发言", 3),
		})
	}
	summary := mergeContextSummary("", events)

	if runes := len([]rune(summary)); runes > contextSummaryMaxRunes {
		t.Fatalf("summary = %d runes, cap is %d", runes, contextSummaryMaxRunes)
	}
	if strings.HasPrefix(summary, "...") {
		t.Fatalf("summary was character-truncated instead of dropping whole lines: %q", summary[:60])
	}
	header, body := splitContextSummary(summary)
	if !strings.HasPrefix(header, contextSummaryHeaderPrefix) {
		t.Fatalf("watermark lost at the cap: %q", header)
	}
	if _, count := parseContextSummaryHeader(header); count != 400 {
		t.Fatalf("watermark count = %d, want 400 even after dropping lines", count)
	}
	if len(body) == 0 {
		t.Fatal("every line was dropped")
	}
	// 每一条留下来的记录都必须仍然完整：以「发送者: 」开头。
	for _, line := range body {
		if !strings.Contains(line, ": ") || !strings.HasPrefix(line, "成员") {
			t.Fatalf("line was cut mid-record: %q", line)
		}
	}
}

func TestFitOlderSummaryRecompressesInsteadOfTruncating(t *testing.T) {
	header := contextSummaryHeader("2026-08-18 09:00", "2026-08-19 08:40", 120)
	summary := longContextSummary(header, 40)
	short := header + "\nAlice 和 Bob 敲定了周五上线，Carol 负责回归测试。"
	provider := &stubSummaryLLMProvider{reply: short}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	budget := int64(256)
	fitted, recompressed := runtime.fitOlderSummaryToBudget(context.Background(), summary, budget, BotConfig{})

	if !recompressed {
		t.Fatal("oversized summary was not reported as recompressed")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one compaction request, got %d", len(provider.requests))
	}
	if cost := llm.EstimateTextTokens(fitted); cost > budget {
		t.Fatalf("recompressed summary costs %d tokens, budget is %d", cost, budget)
	}
	if !strings.HasPrefix(fitted, header) {
		t.Fatalf("watermark lost during recompression: %q", fitted)
	}
	if strings.Contains(fitted, "...[上下文已按 token 预算裁剪]...") {
		t.Fatalf("summary went through the generic truncator: %q", fitted)
	}
}

func TestFitOlderSummaryDropsWholeLinesWhenModelIsUnavailable(t *testing.T) {
	header := contextSummaryHeader("2026-08-18 09:00", "2026-08-19 08:40", 40)
	summary := longContextSummary(header, 40)
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	fitted, recompressed := runtime.fitOlderSummaryToBudget(context.Background(), summary, 512, BotConfig{})

	if !recompressed {
		t.Fatal("structural reduction should still be reported as recompressed")
	}
	fittedHeader, body := splitContextSummary(fitted)
	if fittedHeader != header {
		t.Fatalf("watermark lost: %q", fittedHeader)
	}
	if len(body) == 0 || len(body) >= 40 {
		t.Fatalf("expected some whole lines to be dropped, kept %d", len(body))
	}
	for _, line := range body {
		if !strings.HasPrefix(line, "成员") {
			t.Fatalf("line was cut mid-record: %q", line)
		}
	}
}

func TestFitOlderSummaryKeepsShortSummaryUntouched(t *testing.T) {
	header := contextSummaryHeader("2026-08-18 09:00", "2026-08-19 08:40", 3)
	summary := header + "\nAlice: 周五上线\nBob: 我来回归测试"
	provider := &stubSummaryLLMProvider{reply: "不应该被调用"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	fitted, recompressed := runtime.fitOlderSummaryToBudget(context.Background(), summary, 4096, BotConfig{})

	if recompressed || fitted != summary {
		t.Fatalf("summary within budget was rewritten: recompressed=%v, %q", recompressed, fitted)
	}
	if len(provider.requests) != 0 {
		t.Fatal("a summary that already fits must not cost a model call")
	}
}
