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

// Keep a snippet around the match, including dates located late in OCR text.
func historyMatchSnippet(text, query string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	lower := strings.ToLower(string(runes))
	index := strings.Index(lower, strings.ToLower(query))
	start := 0
	if index >= 0 {
		position := len([]rune(lower[:index]))
		start = max(0, position-limit/3)
	}
	start = min(start, max(0, len(runes)-limit))
	end := min(len(runes), start+limit)
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}

func (t *dianaChatHistoryTool) searchSnippets(ctx context.Context, events []MessageEvent, query string) []dianaChatHistoryItem {
	items := t.items(ctx, events)
	terms := append([]string{query}, structuredMemorySearchTerms(query, 12)...)
	for i := range items {
		item := &items[i]
		full := historyToolEventText(events[i])
		item.Text = historyMatchSnippet(full, query, 220)
		item.TextTruncated = strings.TrimSpace(full) != item.Text
		item.Sender = historyMatchSnippet(item.Sender, "", 64)
		item.QuotedText = ""
		item.QuotedTextTruncated = events[i].Quoted != nil && historyToolQuotedText(events[i].Quoted) != ""
		item.QuotedImageDescriptions = nil
		descriptions := item.ImageDescriptions
		item.ImageDescriptions = nil
		for _, description := range descriptions {
			for _, term := range terms {
				if term != "" && strings.Contains(strings.ToLower(description), strings.ToLower(term)) {
					item.ImageDescriptions = []string{historyMatchSnippet(description, term, 180)}
					break
				}
			}
			if len(item.ImageDescriptions) > 0 {
				break
			}
		}
		item.MediaDetailsOmitted = item.ImageCount > 0 || item.QuotedImageCount > 0 || item.VideoCount > 0
	}
	return items
}

func marshalHistoryResultWithBudget(result dianaChatHistoryResult) (string, error) {
	// Work on a copy: formatting must not consume the caller's evidence.
	copyJSON, err := json.Marshal(result.Items)
	if err != nil {
		return "", err
	}
	var cloned []dianaChatHistoryItem
	if err := json.Unmarshal(copyJSON, &cloned); err != nil {
		return "", err
	}
	result.Items = cloned
	original := len(result.Items)
	upstreamLimited := result.Limited
	mark := func(reason string) {
		result.Truncated = true
		result.Limited = true
		result.TruncationReasons = appendUniqueStrings(result.TruncationReasons, reason)
	}
	if result.Limited {
		mark("upstream_result_limit")
	}
	for _, item := range result.Items {
		if item.TextTruncated {
			mark("text_snippets")
		}
		if item.QuotedTextTruncated {
			mark("quoted_text_shortened")
		}
		if item.MediaDetailsOmitted {
			mark("media_details_omitted")
		}
	}
	if result.Guidance == "" {
		result.Guidance = "truncated=true 表示内容不完整，truncation_reasons 说明省略内容；不要把缺失当作不存在，也不要仅凭这一页断言最早记录。需要细节时用 around，或缩小 range 时间范围继续核对。"
	}
	result.Guidance += " returned_count 是本页实际条数；clipped_count 是本页因预算裁掉的条数；omitted_count 是全部匹配中本页未展示的条数（含先前页）；remaining_count 是游标后待查条数。truncated 和 truncation_reasons 说明正文、媒体或前后文的省略，不能据此认定没有相关证据。"
	refresh := func() {
		result.ReturnedCount = len(result.Items)
		result.ClippedCount = original - len(result.Items)
		result.OmittedCount = max(0, result.Total-len(result.Items))
		if result.searchPage != nil {
			result.TotalIsExact = true
			page := *result.searchPage
			result.Offset = page.Offset
			result.RemainingCount = max(0, result.Total-page.Offset-len(result.Items))
			result.HasMore = result.RemainingCount > 0
			result.SearchComplete = !result.HasMore
			result.Limited = result.Limited || result.HasMore
			result.NextCursor = ""
			if result.HasMore && len(result.Items) > 0 {
				page.Offset += len(result.Items)
				result.NextCursor = page.encode()
				if result.Action == "range" {
					result.NextFromTime = 0
					last := result.Items[len(result.Items)-1].Time
					if page.Offset < len(result.rangeTimes) && result.rangeTimes[page.Offset] > last {
						result.NextFromTime = last + 1
					}
				}
			}
		} else {
			result.HasMore = upstreamLimited || result.OmittedCount > 0
			result.RemainingCount = result.OmittedCount
			result.TotalIsExact = result.Action != "search"
		}
	}
	for stage := 0; ; stage++ {
		refresh()
		body, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		if len([]rune(string(body))) <= maximumChatHistoryOutputRunes {
			return string(body), nil
		}
		switch stage {
		case 0:
			for i := range result.Items {
				item := &result.Items[i]
				if len(item.ContextBefore)+len(item.ContextAfter) > 0 {
					item.ContextBefore, item.ContextAfter = nil, nil
					mark("surrounding_context_removed")
				}
			}
		case 1:
			for i := range result.Items {
				item := &result.Items[i]
				if len(item.ImageDescriptions)+len(item.QuotedImageDescriptions) > 0 {
					changed := false
					for j, text := range item.ImageDescriptions {
						item.ImageDescriptions[j] = historyMatchSnippet(text, result.Query, 100)
						changed = changed || item.ImageDescriptions[j] != text
					}
					for j, text := range item.QuotedImageDescriptions {
						item.QuotedImageDescriptions[j] = historyMatchSnippet(text, result.Query, 80)
						changed = changed || item.QuotedImageDescriptions[j] != text
					}
					if changed {
						item.MediaDetailsOmitted = true
						mark("media_descriptions_shortened")
					}
				}
			}
		case 2:
			for i := range result.Items {
				item := &result.Items[i]
				short := historyMatchSnippet(item.Text, result.Query, 100)
				if short != item.Text {
					item.Text, item.TextTruncated = short, true
					mark("text_shortened")
				}
				quoted := historyMatchSnippet(item.QuotedText, result.Query, 80)
				if quoted != item.QuotedText {
					item.QuotedText = quoted
					item.QuotedTextTruncated = true
					mark("quoted_text_shortened")
				}
			}
		case 3:
			for i := range result.Items {
				item := &result.Items[i]
				if len(item.ImageDescriptions)+len(item.QuotedImageDescriptions) > 0 {
					item.ImageDescriptions, item.QuotedImageDescriptions = nil, nil
					item.MediaDetailsOmitted = true
					mark("media_descriptions_removed")
				}
			}
		default:
			if len(result.Items) <= 1 {
				return "", fmt.Errorf("历史结果元数据过长，无法在预算内保留引用；请缩小查询")
			}
			anchor := -1
			if result.AnchorMessageID != "" {
				for i := range result.Items {
					if result.Items[i].MessageID == result.AnchorMessageID {
						anchor = i
						break
					}
				}
			}
			if anchor >= 0 && anchor >= len(result.Items)-1-anchor {
				result.Items = result.Items[1:]
			} else {
				result.Items = result.Items[:len(result.Items)-1]
			}
			if result.searchPage != nil {
				mark("matches_deferred_to_next_page")
			} else {
				mark("matches_omitted")
			}
		}
	}
}
