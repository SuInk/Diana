// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

type historySearchCursor struct {
	Version int    `json:"v"`
	Scope   string `json:"s"`
	From    int64  `json:"f"`
	Through int64  `json:"t"`
	Offset  int    `json:"o"`
}

func historySearchScope(session, query, scope, order string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{session, query, scope, order}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func decodeHistorySearchCursor(token string) (historySearchCursor, error) {
	var page historySearchCursor
	if len(token) > 1024 {
		return page, fmt.Errorf("cursor too long")
	}
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return page, err
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return page, err
	}
	if page.Version != 1 || len(page.Scope) != 64 || page.Offset < 0 || page.From < 0 || page.Through < page.From {
		return page, fmt.Errorf("invalid cursor")
	}
	return page, nil
}

func (page historySearchCursor) encode() string {
	body, _ := json.Marshal(page)
	return base64.RawURLEncoding.EncodeToString(body)
}

// marshalHistoryResultPage 照 Pi 的做法把结果装进单次上限：条目一律给原文，不缩短
// 正文、不删图片描述、不抽样；装不下的整条留给下一页（search/range 走 next_cursor，
// 其余操作列进 unreturned_message_ids），并在 message 里写明本页覆盖哪段、还剩多少、
// 怎么接着读。
//
// 以前按 7600 字预算先砍前后文、再缩短图片描述和正文，最后从尾部丢条目，丢掉的只记
// 在 clipped_count 里。生产 2026-11-14：模型要 50 条，只拿到 23:53～23:59 的 18 条，
// 它没翻页，按自己猜的范围改查 23 点以前，正好跳过 23:39 那条要找的预测，回答「没看到」。
func marshalHistoryResultPage(ctx context.Context, result dianaChatHistoryResult) (string, error) {
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	all := result.Items
	anchor := -1
	for i := range all {
		if result.AnchorMessageID != "" && all[i].MessageID == result.AnchorMessageID {
			anchor = i
			break
		}
	}
	// [low, high) 是本次装下的条目。分页结果从尾部收；around 从离锚点远的一侧收。
	low, high := 0, len(all)
	for {
		page := historyResultPage(result, all, low, high)
		body, err := json.Marshal(page)
		if err != nil {
			return "", err
		}
		if utf8.RuneCount(body) <= budget || high-low <= 1 {
			return string(body), nil
		}
		if anchor >= 0 && anchor-low > high-1-anchor {
			low++
		} else {
			high--
		}
	}
}

// historyResultPage 填好只装 all[low:high] 时的分页字段和续读说明。
func historyResultPage(result dianaChatHistoryResult, all []dianaChatHistoryItem, low, high int) dianaChatHistoryResult {
	result.Items = all[low:high]
	result.ReturnedCount = len(result.Items)
	cut := len(all) - len(result.Items)
	if result.searchPage != nil {
		page := *result.searchPage
		result.TotalIsExact = true
		result.Offset = page.Offset
		result.RemainingCount = max(0, result.Total-page.Offset-len(result.Items))
		result.HasMore = result.RemainingCount > 0
		result.SearchComplete = !result.HasMore
		result.Limited = result.Limited || result.HasMore
		if result.HasMore && len(result.Items) > 0 {
			page.Offset += len(result.Items)
			result.NextCursor = page.encode()
			if result.Action == "range" {
				last := result.Items[len(result.Items)-1].Time
				if page.Offset < len(result.rangeTimes) && result.rangeTimes[page.Offset] > last {
					result.NextFromTime = last + 1
				}
			}
		}
	} else {
		result.TotalIsExact = result.Action != "search"
		for _, item := range append(append([]dianaChatHistoryItem(nil), all[:low]...), all[high:]...) {
			result.UnreturnedIDs = append(result.UnreturnedIDs, item.MessageID)
		}
		result.RemainingCount = max(len(result.UnreturnedIDs), result.Total-len(result.Items))
		result.HasMore = result.Limited || result.RemainingCount > 0
	}
	if result.HasMore && len(result.Items) > 0 {
		first, last := result.Items[0].LocalTime, result.Items[len(result.Items)-1].LocalTime
		if first != "" && last != "" {
			result.PageWindow = first + " ~ " + last
		}
	}
	if cut > 0 {
		next := "传 cursor=next_cursor、其他参数不变接着读"
		if result.searchPage == nil {
			next = "把 unreturned_message_ids 分批传给 ids 读原文"
		}
		result.Message = strings.TrimSpace(result.Message + fmt.Sprintf(" 单次结果上限 %d 字，本页装下 %d 条（%s），另有 %d 条没装下，%s；没读完之前不要断言没有。", agent.ToolOutputMaxChars, len(result.Items), result.PageWindow, cut, next))
	}
	return result
}
