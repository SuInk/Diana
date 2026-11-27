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

// ReadingPageMaxChars 是搜索、读网页这类工具交给模型的每页上限，比 ToolOutputMaxChars
// 小得多：以前一页给满 2 万字，大半轮次只用到开头几条命中或几段正文，剩下的整页
// 跟着历史一直占输入 token、也挤掉缓存。小页里写明 next_offset 和还剩多少，模型
// 要更多再翻页。
const ReadingPageMaxChars = 5000

// readingPageBudget 返回搜索、读网页类工具这次的每页上限。模型直接调用时
// （Runner 给的是 ToolOutputMaxChars）收到 ReadingPageMaxChars；codemode 脚本里的
// 子调用自带更大的上限（codeModeChildOutputBudget），由脚本自己筛，原样保留；
// 测试或调用方给了更小的上限就用更小的。
func readingPageBudget(ctx context.Context) int {
	budget := ToolOutputBudget(ctx)
	switch {
	case budget > ToolOutputMaxChars:
		return budget
	case budget <= 0:
		return ReadingPageMaxChars
	default:
		return min(budget, ReadingPageMaxChars)
	}
}
