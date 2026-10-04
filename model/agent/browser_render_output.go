// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// browser_render 交给模型的输出：正文之外附上页面链接和页内查找结果。
//
// 以前只给正文，链接只留给搜索引擎后端用。10-01 问 DimAgent 的价格，模型打开英文
// 首页，导航上写着「Docs」却拿不到它链到哪，只能猜网址：猜 /docs/pricing 落到账户
// 中心，第一轮就断言「没有付费套餐」，第二轮才撞上 /docs/credits。Codex 的 web.run
// 有 open / click / find：打开页面后按编号点链接、在页内找关键词，不用猜。这里不留
// 跨调用的状态，直接把链接的真实网址交给模型，下一步照抄网址打开即可。
const (
	// browserRenderMaxLinks 是交给模型的链接数：300 个全给太占上下文，导航、文档目录
	// 和正文里的关键链接通常在前几十个里。
	browserRenderMaxLinks    = 40
	browserRenderLinkText    = 60
	browserRenderMaxFindTerm = 8
	browserRenderMaxMatches  = 8
	// browserRenderMatchRadius 是命中处前后各取多少字：够看清一条价格或一段规则。
	browserRenderMatchRadius = 200
)

type browserRenderPayload struct {
	RenderedPage
	Links       []string `json:"links,omitempty"`
	FindMatches []string `json:"find_matches,omitempty"`
	FindNote    string   `json:"find_note,omitempty"`
	ReadNotice  string   `json:"read_notice,omitempty"`
}

func browserRenderOutput(page RenderedPage, find string) (string, error) {
	return browserRenderOutputWithBudget(page, find, DefaultMaxToolOutputChars)
}

func browserRenderOutputWithBudget(page RenderedPage, find string, budget int) (string, error) {
	terms := browserRenderFindTerms(find)
	payload := browserRenderPayload{RenderedPage: page, Links: browserRenderLinks(page, terms)}
	if len(terms) > 0 {
		text := page.FullText
		if text == "" {
			text = page.Text
		}
		payload.FindMatches = browserRenderFindMatches(text, terms)
		if len(payload.FindMatches) == 0 {
			payload.FindNote = "整页正文里没找到 " + strings.Join(terms, "、") + "；页面没写不等于没有，可以从 links 打开相关页面接着找。"
		}
	}
	for {
		if payload.Truncated && len(terms) == 0 {
			payload.ReadNotice = `正文已截断；若需要未出现的细节，请对本页再次调用 browser_render，参数为 {"url":"本次返回的 url","find":"所需关键词，可用 | 分隔"}。不要用重复搜索代替页内查找，也不能凭印象补出未读取的机制。`
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return "", err
		}
		if len([]rune(string(data))) <= budget {
			return string(data), nil
		}
		// 先去掉低优先级链接和元数据，保留正文及 find 真正命中的段落。
		if len(payload.Links) > 5 {
			payload.Links = payload.Links[:len(payload.Links)-1]
		} else if payload.Description != "" {
			payload.Description = ""
		} else if payload.Text != "" {
			payload.Text = string([]rune(payload.Text)[:len([]rune(payload.Text))/2])
			payload.Truncated = true
		} else if len(payload.FindMatches) > 1 {
			payload.FindMatches = payload.FindMatches[:len(payload.FindMatches)-1]
		} else if len(payload.FindMatches) == 1 && payload.FindMatches[0] != "" {
			match := []rune(payload.FindMatches[0])
			payload.FindMatches[0] = string(match[:len(match)/2])
			payload.Truncated = true
		} else if len(payload.Links) > 0 {
			payload.Links = payload.Links[:len(payload.Links)-1]
		} else {
			return string(data), nil
		}
	}
}

func browserRenderFindTerms(find string) []string {
	var terms []string
	seen := map[string]bool{}
	for _, term := range strings.Split(find, "|") {
		term = strings.TrimSpace(term)
		key := strings.ToLower(term)
		if term == "" || seen[key] {
			continue
		}
		seen[key] = true
		terms = append(terms, term)
		if len(terms) >= browserRenderMaxFindTerm {
			break
		}
	}
	return terms
}

// browserRenderLinks 按「命中 find 的、同站的、其余的」排序，去掉页内锚点和重复地址，
// 每条写成「文字 | 网址」。
func browserRenderLinks(page RenderedPage, terms []string) []string {
	if len(page.Links) == 0 {
		return nil
	}
	pageURL := strings.TrimSpace(page.URL)
	if pageURL == "" {
		pageURL = page.RequestedURL
	}
	base, _ := url.Parse(pageURL)
	type candidate struct {
		line  string
		rank  int
		order int
	}
	var candidates []candidate
	seen := map[string]bool{}
	if base != nil {
		seen[withoutFragment(base)] = true
	}
	for index, link := range page.Links {
		target, err := url.Parse(link.URL)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
			continue
		}
		key := withoutFragment(target)
		if seen[key] {
			continue
		}
		seen[key] = true
		rank := 2
		if base != nil && sameSite(base.Hostname(), target.Hostname()) {
			rank = 1
		}
		if containsAnyFold(link.Text+" "+link.URL, terms) {
			rank = 0
		}
		text := truncateText(strings.Join(strings.Fields(link.Text), " "), browserRenderLinkText)
		candidates = append(candidates, candidate{line: text + " | " + key, rank: rank, order: index})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].order < candidates[j].order
	})
	if len(candidates) > browserRenderMaxLinks {
		candidates = candidates[:browserRenderMaxLinks]
	}
	lines := make([]string, len(candidates))
	for index, item := range candidates {
		lines[index] = item.line
	}
	return lines
}

// browserRenderFindMatches 在正文里找关键词，取命中处前后的段落；相邻的命中合并成一段。
func browserRenderFindMatches(text string, terms []string) []string {
	runes := []rune(text)
	lower := []rune(strings.ToLower(text))
	if len(lower) != len(runes) {
		// 大小写转换改变了长度（极少数字符），退回按原文匹配，下标才对得上。
		lower = runes
	}
	type span struct{ start, end int }
	var spans []span
	for _, term := range terms {
		needle := []rune(strings.ToLower(term))
		if len(needle) == 0 {
			continue
		}
		for index := 0; index+len(needle) <= len(lower); index++ {
			if !runesEqualAt(lower, needle, index) {
				continue
			}
			spans = append(spans, span{max(0, index-browserRenderMatchRadius), min(len(runes), index+len(needle)+browserRenderMatchRadius)})
			index += len(needle) - 1
		}
	}
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := []span{spans[0]}
	for _, item := range spans[1:] {
		last := &merged[len(merged)-1]
		if item.start <= last.end {
			last.end = max(last.end, item.end)
			continue
		}
		merged = append(merged, item)
	}
	if len(merged) > browserRenderMaxMatches {
		merged = merged[:browserRenderMaxMatches]
	}
	out := make([]string, len(merged))
	for index, item := range merged {
		excerpt := strings.TrimSpace(string(runes[item.start:item.end]))
		if item.start > 0 {
			excerpt = "…" + excerpt
		}
		if item.end < len(runes) {
			excerpt += "…"
		}
		out[index] = excerpt
	}
	return out
}

func runesEqualAt(haystack, needle []rune, at int) bool {
	for offset, char := range needle {
		if haystack[at+offset] != char {
			return false
		}
	}
	return true
}

func containsAnyFold(text string, terms []string) bool {
	lower := strings.ToLower(text)
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func withoutFragment(target *url.URL) string {
	copied := *target
	copied.Fragment = ""
	copied.RawFragment = ""
	return copied.String()
}

// sameSite 粗略判断同站：主机名相同，或末两段相同（docs.example.com 和 example.com）。
func sameSite(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	return lastLabels(a, 2) != "" && lastLabels(a, 2) == lastLabels(b, 2)
}

func lastLabels(host string, count int) string {
	parts := strings.Split(host, ".")
	if len(parts) < count {
		return ""
	}
	return strings.Join(parts[len(parts)-count:], ".")
}
