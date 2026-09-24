// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
)

// OutputBudgetTool 让工具声明单次结果需要的字数上限。
//
// Runner 默认把每次工具结果截到 MaxToolOutputChars。读代码、读 diff 的工具按自己
// 的上限分页：github 的 read_file 按 1500 行设计、pull_files 按 6 万字设计，被
// 8000 字一刀切在 JSON 中间，续读提示里的行号也跟着错了。声明了上限的工具按
// 声明值截（仍受 MaxAllowedToolOutputChars 约束），并能从 ToolOutputBudget 读到
// 这次实际给了多少，自己在边界内分页。
type OutputBudgetTool interface {
	Tool
	MaxOutputChars() int
}

type toolOutputBudgetKey struct{}

// ToolOutputBudget 返回 Runner 给这次调用的结果字数上限；不在 Runner 里调用时返回 0。
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

// toolOutputLimit 算出这次工具结果的截断上限：默认用配置值，工具声明了更大的
// 上限就按声明值，但不超过全局硬上限。声明值只能放宽，不能收紧——收紧没有意义，
// 工具自己少返回就行。
func (r *Runner) toolOutputLimit(tool Tool) int {
	limit := r.cfg.MaxToolOutputChars
	if budgeted, ok := tool.(OutputBudgetTool); ok {
		if want := budgeted.MaxOutputChars(); want > limit {
			limit = min(want, MaxAllowedToolOutputChars)
		}
	}
	return limit
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
