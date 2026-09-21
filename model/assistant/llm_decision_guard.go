// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 只做判断的模型（Jev 这类 System One 模型）不生成文本。意图识别这一档下面挂着十几个
// 用途，其中上下文压缩、记忆提取、关系评估和几种提示改写都是要出文字的，整档绑过去
// 必然有一半跑不起来。上游会直接拒绝，这里把拒绝翻译成「哪个用途、该怎么改」。
func withDecisionOnlyNoticeRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil {
		return run
	}
	purpose := strings.TrimSpace(llmUsagePurposeFromContext(ctx))
	return func(provider LLMProvider) (string, error) {
		text, err := run(&decisionOnlyNoticeProvider{provider: provider, purpose: purpose})
		return text, err
	}
}

type decisionOnlyNoticeProvider struct {
	provider LLMProvider
	purpose  string
}

func (p *decisionOnlyNoticeProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := p.provider.Generate(ctx, req)
	if err == nil || !errors.Is(err, llm.ErrDecisionRequired) {
		return resp, err
	}
	purpose := p.purpose
	if purpose == "" {
		purpose = "这次调用"
	}
	return nil, fmt.Errorf("绑定的是只做判断的模型，它不生成文本，而用途 %s 需要文本输出：请在模型绑定里把 %s 单独绑到对话模型（%w）", purpose, purpose, err)
}
