// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

// web_search 交给模型的结果照 Pi 的做法分页：命中整条给，不缩短摘要、不丢命中，
// 一次装不下的留到下一页，next_offset 和 read_notice 写明怎么接着读。来源、搜索源、
// 查询这些诊断字段每页都带着。
//
// 以前超过预算时先丢 sources、providers、attempts、queries，再把摘要砍到 300 字，
// 最后从尾部整条丢命中，丢掉的只剩一个 truncated 标记；模型以为搜到的就这些。

const (
	webSearchPageTTL     = 10 * time.Minute
	webSearchPageEntries = 16
)

type webSearchPageEntry struct {
	result  webSearchResult
	created time.Time
}

// webSearchPages 按搜索参数（不含 offset）留最近几次的完整结果。零值可用，nil 不缓存。
type webSearchPages struct {
	mu      sync.Mutex
	entries map[string]webSearchPageEntry
}

func (p *webSearchPages) get(key string) (webSearchResult, bool) {
	if p == nil {
		return webSearchResult{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[key]
	if !ok || time.Since(entry.created) > webSearchPageTTL {
		return webSearchResult{}, false
	}
	return entry.result, true
}

func (p *webSearchPages) put(key string, result webSearchResult) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.entries == nil {
		p.entries = map[string]webSearchPageEntry{}
	}
	oldest := ""
	for k, entry := range p.entries {
		if now.Sub(entry.created) > webSearchPageTTL {
			delete(p.entries, k)
		} else if oldest == "" || entry.created.Before(p.entries[oldest].created) {
			oldest = k
		}
	}
	if len(p.entries) >= webSearchPageEntries && oldest != "" {
		delete(p.entries, oldest)
	}
	p.entries[key] = webSearchPageEntry{result: result, created: now}
}

// webSearchPageArgs 是续读要带的参数：原样的搜索参数去掉 offset。键也用它。
func webSearchPageArgs(input map[string]any) map[string]any {
	args := make(map[string]any, len(input))
	for key, value := range input {
		if key != "offset" {
			args[key] = value
		}
	}
	return args
}

func webSearchPageKey(input map[string]any) string {
	data, _ := json.Marshal(webSearchPageArgs(input))
	return string(data)
}

// webSearchPage 从 offset 起尽量多装：命中按整条，content 按整行（第一行就装不下才
// 从行内切）。位置先数命中，再数 content 的字。至少装一个位置，保证续读往前走。
func webSearchPage(result webSearchResult, args map[string]any, offset, budget int) (string, error) {
	if budget <= 0 {
		budget = ToolOutputMaxChars
	}
	hits := result.Results
	content := []rune(result.Content)
	total := len(hits) + len(content)
	offset = min(max(offset, 0), total)
	encode := func(end int) (string, bool, error) {
		page := result
		page.Results = nil
		page.Content = ""
		page.Offset = offset
		page.TotalResults = len(hits)
		page.ContentChars = len(content)
		if offset < len(hits) {
			page.Results = hits[offset:min(end, len(hits))]
		}
		if end > len(hits) {
			page.Content = string(content[max(offset, len(hits))-len(hits) : end-len(hits)])
		}
		if end < total {
			page.NextOffset = end
			page.ReadNotice = webSearchReadNotice(len(hits), len(content), end, args)
		}
		data, err := json.Marshal(page)
		return string(data), utf8.RuneCount(data) <= budget, err
	}
	var bounds []int
	for position := offset; position < total; {
		if position < len(hits) {
			position++
		} else {
			index := position - len(hits)
			for index < len(content) && content[index] != '\n' {
				index++
			}
			if index < len(content) {
				index++
			}
			position = len(hits) + index
		}
		bounds = append(bounds, position)
	}
	if len(bounds) == 0 {
		out, _, err := encode(offset)
		return out, err
	}
	lo, hi := -1, len(bounds)-1
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if _, ok, err := encode(bounds[mid]); err != nil {
			return "", err
		} else if ok {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	end := bounds[0]
	if lo >= 0 {
		end = bounds[lo]
	} else if offset >= len(hits) {
		low, high := offset+1, bounds[0]
		for low < high {
			mid := low + (high-low+1)/2
			if _, ok, err := encode(mid); err != nil {
				return "", err
			} else if ok {
				low = mid
			} else {
				high = mid - 1
			}
		}
		end = low
	}
	out, _, err := encode(end)
	return out, err
}

func webSearchReadNotice(hits, content, next int, args map[string]any) string {
	left := ""
	if rest := hits - next; rest > 0 {
		left = fmt.Sprintf("还有 %d 条命中", rest)
	}
	if rest := content - max(0, next-hits); rest > 0 && content > 0 {
		if left != "" {
			left += "，"
		}
		left += fmt.Sprintf("搜索源原文还有 %d 字", min(rest, content))
	}
	call := make(map[string]any, len(args)+1)
	for key, value := range args {
		call[key] = value
	}
	call["offset"] = next
	data, _ := json.Marshal(call)
	return "这次结果没给完：" + left + "。需要后面的就再调一次 web_search，参数 " + string(data) + "（其余参数不变，offset 照抄），直接从缓存取，不会重新搜索。没看到的命中不能当成没搜到。"
}
