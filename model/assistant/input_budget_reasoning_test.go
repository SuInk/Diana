// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 会思考的模型先写 reasoning 再写正文。按摘要目标长度卡死 max_output_tokens，额度
// 在思考阶段就用光，正文一个字都写不出来——线上 deepseek-flash 的压缩 53 次里只成功
// 过 1 次。上限要留出思考余量。
func TestSummaryOutputCapLeavesReasoningHeadroom(t *testing.T) {
	for _, tc := range []struct{ window, want int64 }{
		{128000, 64000},
		{16384, 8192},
		// 窗口未知时按默认窗口的一半兜底，不会退回「按目标长度卡死」。
		{0, llm.DefaultContextWindowTokens / 2},
	} {
		if got := summaryOutputCap(tc.window); got != tc.want {
			t.Fatalf("summaryOutputCap(%d) = %d, want %d", tc.window, got, tc.want)
		}
	}
}

type truncatedThenTextProvider struct {
	caps  []int64
	reply string
}

func (p *truncatedThenTextProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.caps = append(p.caps, req.MaxOutputTokens)
	// 思考比预留的余量还长：第一次仍然只有 reasoning，不带上限重试才写得出正文。
	if req.MaxOutputTokens > 0 {
		return nil, fmt.Errorf("responses output is empty after truncation: %w", llm.ErrCompletionTruncatedNoText)
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.reply}, nil
}

func TestSummarizeBudgetTextRetriesWithoutCapWhenReasoningExhaustsOutput(t *testing.T) {
	provider := &truncatedThenTextProvider{reply: "压缩后的摘要"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	summary, err := runtime.summarizeBudgetText(context.Background(), "很长的历史对话", 128, 16384)
	if err != nil || summary != "压缩后的摘要" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if len(provider.caps) != 2 || provider.caps[0] != 8192 || provider.caps[1] != 0 {
		t.Fatalf("caps = %v, want [8192 0]", provider.caps)
	}
}
