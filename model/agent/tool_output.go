// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
)

type toolOutputBudgetKey struct{}

// ToolOutputBudget 返回 Runner 给这次调用的结果字数上限（ToolOutputMaxChars），
// 工具据此按整条分页；不在 Runner 里调用时返回 0。
func ToolOutputBudget(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	budget, _ := ctx.Value(toolOutputBudgetKey{}).(int)
	return budget
}

// WithToolOutputBudget 把结果字数上限挂到 ctx 上。Runner 每次调用工具时设置；
// 测试和直接调用工具的地方也可以用它模拟 Runner 的上限。
func WithToolOutputBudget(ctx context.Context, budget int) context.Context {
	if budget <= 0 {
		return ctx
	}
	return context.WithValue(ctx, toolOutputBudgetKey{}, budget)
}

// truncateToolOutput 截断超长的工具结果，并在末尾说清楚截掉了多少、怎么拿后面的。
//
// 以前只追加一句 ...truncated...：模型不知道原文有多长、缺了多少，常把截断当成
// 全文往下总结。
func truncateToolOutput(output string, limit int) string {
	if limit <= 0 {
		return output
	}
	runes := []rune(output)
	if len(runes) <= limit {
		return output
	}
	return string(runes[:limit]) + fmt.Sprintf("\n...[结果共 %d 字，超出单次上限，只给了前 %d 字，后面 %d 字没有给出。不要把这段当成全文；需要后面的内容就用这个工具的分页、行号或路径参数缩小范围再调一次]", len(runes), limit, len(runes)-limit)
}
