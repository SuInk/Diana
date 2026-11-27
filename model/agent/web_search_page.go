// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

// webSearchPage 从 offset 起尽量多装。位置先数命中，再数 content 的字：一条命中占
// 「1 + 摘要字数」个位置，起点是整条，往后每个位置是摘要里的一个字，所以摘要比整页
// 还长时能从中间切开，续读落在这条命中内部；content 按整行装，一行装不下才从行内切。
// 至少装一个位置，保证续读往前走。
//
// sources、searches、queries、providers、attempts 这些诊断只放第一页：以前每页都带，
// 十个查询时光这些就近一万字。Runner 先按单次上限截原始输出、再给模型精简版：
// 给模型的小页（budget 不超过 ToolOutputMaxChars）按精简版计量，同时保证原始输出
// 不被截；codemode 子调用拿原始输出，按原始输出计量。
func webSearchPage(result webSearchResult, args map[string]any, offset, budget int) (string, error) {
	if budget <= 0 {
		budget = ToolOutputMaxChars
	}
	hits := result.Results
	starts := make([]int, len(hits)+1)
	for i, hit := range hits {
		starts[i+1] = starts[i] + 1 + utf8.RuneCountInString(hit.Snippet)
	}
	hitEnd := starts[len(hits)]
	content := []rune(result.Content)
	total := hitEnd + len(content)
	offset = min(max(offset, 0), total)
	encode := func(end int) (string, bool, error) {
		page := result
		page.Results = nil
		page.Content = ""
		if offset > 0 {
			page.Sources, page.Searches, page.Queries, page.Providers, page.Attempts = nil, nil, nil, nil, nil
		}
		page.Offset = offset
		page.TotalResults = len(hits)
		page.ContentChars = len(content)
		for i, hit := range hits {
			lo, hi := max(offset, starts[i]), min(end, starts[i+1])
			if lo >= hi {
				continue
			}
			snippet := []rune(hit.Snippet)
			from, to := max(lo-starts[i]-1, 0), min(hi-starts[i]-1, len(snippet))
			if lo == starts[i] {
				from = 0
			}
			if hi == starts[i+1] {
				to = len(snippet)
			}
			if from > 0 || to < len(snippet) {
				hit.Snippet = string(snippet[from:max(from, to)])
				hit.SnippetOffset, hit.SnippetChars = from, len(snippet)
			}
			page.Results = append(page.Results, hit)
		}
		if end > hitEnd {
			page.Content = string(content[max(offset, hitEnd)-hitEnd : end-hitEnd])
		}
		if end < total {
			page.NextOffset = end
			page.ReadNotice = webSearchReadNotice(len(hits), len(content), webSearchHitsLeft(starts, end), max(0, total-max(end, hitEnd)), args, end)
		}
		data, err := json.Marshal(page)
		if err != nil || budget > ToolOutputMaxChars {
			return string(data), utf8.RuneCount(data) <= budget, err
		}
		// 模型看到的是去掉诊断字段的精简版，小页按它计量；原始输出仍要在 Runner 的
		// 单次上限以内，否则先被硬截、精简版就解析不出来了。
		fits := utf8.RuneCount(data) <= ToolOutputMaxChars && utf8.RuneCountInString(webSearchOutputForModel(string(data))) <= budget
		return string(data), fits, nil
	}
	var bounds []int
	for position := offset; position < total; {
		if position < hitEnd {
			i := sort.SearchInts(starts, position+1) - 1
			position = starts[i+1]
		} else {
			index := position - hitEnd
			for index < len(content) && content[index] != '\n' {
				index++
			}
			if index < len(content) {
				index++
			}
			position = hitEnd + index
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
	} else {
		// 第一条就装不下：命中从摘要中间切，content 从行内切，取装得下的最长一段。
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

// webSearchHitsLeft 数 end 之后还有几条命中没给完（切在中间的那条也算）。
func webSearchHitsLeft(starts []int, end int) int {
	left := 0
	for i := 0; i+1 < len(starts); i++ {
		if starts[i+1] > end {
			left++
		}
	}
	return left
}

func webSearchReadNotice(hits, content, hitsLeft, contentLeft int, args map[string]any, next int) string {
	var left []string
	if hitsLeft > 0 {
		left = append(left, fmt.Sprintf("命中还有 %d 条（共 %d 条）", hitsLeft, hits))
	}
	if contentLeft > 0 && content > 0 {
		left = append(left, fmt.Sprintf("搜索源原文还有 %d 字", contentLeft))
	}
	call := make(map[string]any, len(args)+1)
	for key, value := range args {
		call[key] = value
	}
	call["offset"] = next
	data, _ := json.Marshal(call)
	return "这次结果没给完：" + strings.Join(left, "，") + "。需要后面的就再调一次 web_search，参数 " + string(data) + "（其余参数不变，offset 照抄）。" +
		fmt.Sprintf("%d 分钟内翻页直接取这次的结果，不重新搜索；过了时间会重新搜，结果可能和这一页对不上。", int(webSearchPageTTL.Minutes())) +
		"没看到的命中不能当成没搜到。"
}
