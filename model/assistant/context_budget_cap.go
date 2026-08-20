// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"

	"github.com/SuInk/diana/model/llm"
)

type contextBudgetCapKey struct{}

// withContextBudgetCap 把当前机器人/群配置的单次请求上下文上限放进 ctx。
// 只在提示词编排里收紧预算是不够的：Agent 的多轮工具调用会自己往请求里追加
// 内容，最终请求还是按配置档的窗口结算。上限必须一路带到真正的 Generate 调用。
func withContextBudgetCap(ctx context.Context, tokens int64) context.Context {
	if ctx == nil || tokens <= 0 {
		return ctx
	}
	return context.WithValue(ctx, contextBudgetCapKey{}, tokens)
}

func contextBudgetCapFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	tokens, _ := ctx.Value(contextBudgetCapKey{}).(int64)
	return tokens
}

// withContextBudgetCapRun 让这条链路上的每次请求都带上上限。GenerateRequest 的
// MaxContextTokens 只接受更保守的值，所以这里设大了也不会放宽配置档。
func (r *Runtime) withContextBudgetCapRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	cap := contextBudgetCapFromContext(ctx)
	if cap <= 0 {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&contextBudgetCapProvider{provider: provider, maxContextTokens: cap})
	}
}

type contextBudgetCapProvider struct {
	provider         LLMProvider
	maxContextTokens int64
}

func (p *contextBudgetCapProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	// 超限收缩重试会设一个更小的值，不要把它顶回去。
	if req.MaxContextTokens <= 0 || p.maxContextTokens < req.MaxContextTokens {
		req.MaxContextTokens = p.maxContextTokens
	}
	return p.provider.Generate(ctx, req)
}
