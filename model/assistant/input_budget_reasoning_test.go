// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type capturingSummaryProvider struct {
	requests []llm.GenerateRequest
}

func (p *capturingSummaryProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "压缩后的摘要"}, nil
}

// 摘要压缩不下发 max_output_tokens。它管的是总输出，会思考的模型先写 reasoning 再写
// 正文，按摘要目标长度卡死就会让正文一个字都写不出来：线上 deepseek-flash 的压缩 53 次
// 里只成功过 1 次。目标长度只写在提示词里。
func TestSummarizeBudgetTextSendsNoOutputCap(t *testing.T) {
	provider := &capturingSummaryProvider{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	summary, err := runtime.summarizeBudgetText(context.Background(), "很长的历史对话", 128)
	if err != nil || summary != "压缩后的摘要" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(provider.requests))
	}
	if got := provider.requests[0].MaxOutputTokens; got != 0 {
		t.Fatalf("MaxOutputTokens = %d, want 0（不下发上限）", got)
	}
}
