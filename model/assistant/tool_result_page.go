// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// toolResultPage 是列表类工具结果里的分页字段，照 Pi 的做法：条数由模型传 limit、
// 不设上限，条目一律给原文；装不下单次上限的整条留给下一页，并写明还剩多少、
// 怎么接着读。不再按条数硬上限截断、按字数预算丢尾部或缩短正文。
type toolResultPage struct {
	Total          int    `json:"total"`
	Offset         int    `json:"offset"`
	ReturnedCount  int    `json:"returned_count"`
	RemainingCount int    `json:"remaining_count"`
	HasMore        bool   `json:"has_more"`
	NextOffset     int    `json:"next_offset,omitempty"`
	PageNote       string `json:"page_note,omitempty"`
}

// toolPageOffset 读模型给的 offset（从 0 开始），负数按 0。
func toolPageOffset(input map[string]any) int {
	return max(0, intFromAny(input["offset"]))
}

// marshalToolResultPage 从第 offset 条起最多取 limit 条（limit<=0 表示不限），装进
// 单次结果上限。build(n, page) 返回只装 items[offset:offset+n] 时的完整结果，page 已填好
// 分页字段，build 把它嵌进结果即可。单条本身超过上限时仍整条给出，交给 Runner 统一
// 处理，不在这里缩短。
func marshalToolResultPage(ctx context.Context, total, offset, limit int, build func(n int, page toolResultPage) any) (string, error) {
	offset = min(max(0, offset), total)
	want := total - offset
	if limit > 0 {
		want = min(want, limit)
	}
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	encode := func(n int) (string, bool, error) {
		body, err := json.Marshal(build(n, toolResultPageFor(total, offset, n, want)))
		if err != nil {
			return "", false, err
		}
		return string(body), utf8.RuneCount(body) <= budget, nil
	}
	body, fits, err := encode(want)
	if err != nil || fits || want <= 1 {
		return body, err
	}
	// 二分出装得下的最多条数；一条都装不下时也给一条。
	low, high := 2, want-1
	best, _, err := encode(1)
	if err != nil {
		return "", err
	}
	for low <= high {
		mid := (low + high) / 2
		candidate, ok, err := encode(mid)
		if err != nil {
			return "", err
		}
		if ok {
			best, low = candidate, mid+1
		} else {
			high = mid - 1
		}
	}
	return best, nil
}

// toolResultPageFor 填好分页字段。want 是这次本想给的条数（limit 和剩余条数取小），
// n 是实际装下的条数；n<want 说明是单次上限装不下。
func toolResultPageFor(total, offset, n, want int) toolResultPage {
	page := toolResultPage{Total: total, Offset: offset, ReturnedCount: n, RemainingCount: max(0, total-offset-n)}
	page.HasMore = page.RemainingCount > 0
	if !page.HasMore {
		if offset > 0 && n == 0 {
			page.PageNote = fmt.Sprintf("共 %d 条，offset=%d 已经超出末尾。", total, offset)
		}
		return page
	}
	page.NextOffset = offset + n
	reason := ""
	if n < want {
		reason = fmt.Sprintf("单次结果上限 %d 字只装下 %d 条，", agent.ToolOutputMaxChars, n)
	}
	page.PageNote = fmt.Sprintf("共 %d 条，本页是第 %d～%d 条，%s还剩 %d 条没给：传 offset=%d、其他参数不变接着读（也可加大 limit）；没读完之前不要断言没有。",
		total, offset+1, offset+n, reason, page.RemainingCount, page.NextOffset)
	return page
}
