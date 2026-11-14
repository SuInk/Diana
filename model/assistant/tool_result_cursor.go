// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// KeysetCursor 是按 (created_at, id) 新到旧键集翻页的位置：下一页只取严格排在它后面
// （更早，或同一时刻 id 更小）的条目。
//
// offset 翻页在列表会变的地方会跳条：待处理请求被同意/拒绝后离开列表，后面的整体
// 前移，按旧 offset 续读就跳过一批；按 usage_count 排的笔记本在翻页间被异步加一，
// 名次一换就有条目被跳过或读两遍。键集游标只认已经给过的最后一条，前面的条目离开、
// 计数变化或新条目到来都不影响后面的页。
type KeysetCursor struct {
	At time.Time
	ID string
}

type keysetCursorToken struct {
	Version int    `json:"v"`
	At      int64  `json:"t"`
	ID      string `json:"i"`
}

func (c KeysetCursor) encode() string {
	body, _ := json.Marshal(keysetCursorToken{Version: 1, At: c.At.UnixNano(), ID: c.ID})
	return base64.RawURLEncoding.EncodeToString(body)
}

// toolKeysetCursor 读模型传回的 cursor；没传时是 nil（从第一条读起）。
func toolKeysetCursor(input map[string]any) (*KeysetCursor, error) {
	token := strings.TrimSpace(configToolString(input, "cursor"))
	if token == "" {
		return nil, nil
	}
	invalid := fmt.Errorf("cursor 无效：请原样传回上一页结果里的 next_cursor，或不传 cursor 从头读")
	if len(token) > 1024 {
		return nil, invalid
	}
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, invalid
	}
	var decoded keysetCursorToken
	if json.Unmarshal(body, &decoded) != nil || decoded.Version != 1 || decoded.ID == "" {
		return nil, invalid
	}
	return &KeysetCursor{At: time.Unix(0, decoded.At), ID: decoded.ID}, nil
}

// toolKeysetFetchLimit 是这一页从存储层取几条：模型给了 limit 就取 limit 条（不设上限），
// 没给时按单次上限估一个够装满一页的数——一条结果至少几十字，再多取也装不下，只是白读。
func toolKeysetFetchLimit(ctx context.Context, limit int) int {
	if limit > 0 {
		return limit
	}
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	return max(1, budget/50)
}

// toolCursorPage 是键集翻页结果里的分页字段。RemainingCount 是本页之后还没给的条数。
type toolCursorPage struct {
	ReturnedCount  int    `json:"returned_count"`
	RemainingCount int    `json:"remaining_count"`
	HasMore        bool   `json:"has_more"`
	NextCursor     string `json:"next_cursor,omitempty"`
	PageNote       string `json:"page_note,omitempty"`
}

// marshalToolCursorPage 把从游标处取回的 fetched 条装进单次上限。matched 是从游标处
// 起（含本页）一共还有多少条匹配，keyAt(i) 是第 i 条的排序键；build 同
// marshalToolResultPage，只装前 n 条。装不下的整条留给下一页，next_cursor 指向本页最后一条。
func marshalToolCursorPage(ctx context.Context, fetched, matched int, keyAt func(i int) KeysetCursor, build func(n int, page toolCursorPage) any) (string, error) {
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	return marshalToolResultPage(ctx, fetched, 0, 0, func(n int, _ toolResultPage) any {
		return build(n, toolCursorPageFor(n, fetched, matched, budget, keyAt))
	})
}

func toolCursorPageFor(n, fetched, matched, budget int, keyAt func(i int) KeysetCursor) toolCursorPage {
	// 计数和列表是两次查询，中间新到的条目可能让 fetched 比 matched 多，取大的。
	page := toolCursorPage{ReturnedCount: n, RemainingCount: max(0, matched-n, fetched-n)}
	page.HasMore = page.RemainingCount > 0 && n > 0
	if !page.HasMore {
		return page
	}
	page.NextCursor = keyAt(n - 1).encode()
	reason := ""
	if n < fetched {
		reason = fmt.Sprintf("单次结果上限 %d 字只装下 %d 条，", budget, n)
	}
	page.PageNote = fmt.Sprintf("本页 %d 条，%s还剩 %d 条没给：传 cursor=next_cursor、其他参数不变接着读。游标只认本页最后一条，期间有条目被处理掉或排序变化也不会跳条；没读完之前不要断言没有。",
		n, reason, page.RemainingCount)
	return page
}
