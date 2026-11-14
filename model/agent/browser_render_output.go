// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// browser_render 交给模型的输出：正文之外附上页面链接和页内查找结果。
//
// 以前只给正文，链接只留给搜索引擎后端用。10-01 问 DimAgent 的价格，模型打开英文
// 首页，导航上写着「Docs」却拿不到它链到哪，只能猜网址：猜 /docs/pricing 落到账户
// 中心，第一轮就断言「没有付费套餐」，第二轮才撞上 /docs/credits。Codex 的 web.run
// 有 open / click / find：打开页面后按编号点链接、在页内找关键词，不用猜。这里不留
// 跨调用的状态，直接把链接的真实网址交给模型，下一步照抄网址打开即可。
const (
	browserRenderMaxFindTerm = 8
	// browserRenderMatchContext 是命中处前后各带几行：够看清一条价格或一段规则。
	browserRenderMatchContext = 1
)

type browserRenderPayload struct {
	// 分页字段放在最前面：单条过长被 Runner 硬截时，续读方式也不会被截掉。
	// Offset 是这一页从哪里开始；TotalPositions 是整页有多少个位置（查找命中、正文
	// 每个字、每条链接、每张图片、每个跳转前页面各占一个）；NextOffset 非零表示后面
	// 还有，照抄进 offset 接着读。
	Offset         int    `json:"offset,omitempty"`
	TotalPositions int    `json:"total_positions"`
	NextOffset     int    `json:"next_offset,omitempty"`
	ReadNotice     string `json:"read_notice,omitempty"`
	RenderedPage
	Links       []string `json:"links,omitempty"`
	FindMatches []string `json:"find_matches,omitempty"`
	// FindOffsets 和 FindMatches 一一对应：命中段落在正文里的位置，填进 offset 就从那里读正文。
	FindOffsets []int  `json:"find_offsets,omitempty"`
	FindNote    string `json:"find_note,omitempty"`
}

// browserRenderSequence 是一页的全部内容，按查找命中、正文、链接、图片、跳转前页面
// 排成一列。照 Pi 的做法分页：每次从 offset 起按整条装进单次上限，装不下的留到下一
// 页并写明 next_offset，不缩短、不抽样、不丢。正文按字计位置，一次装不下时按整行切开；
// 一行比整页上限还长才从行内切，下一页从切口接上。
//
// 以前正文截到 8000 字，JSON 超预算时先砍图片、砍链接再把正文减半；链接只给 40 条、
// 每条 60 字，find 只给 8 段、每段前后 200 字，跳转前页面只给 3 页、每页 1200 字，
// 批量读取按页数平分预算。模型看到的是被删过的页面，却不知道少了什么。
type browserRenderSequence struct {
	page        RenderedPage
	matches     []string
	matchStarts []int // 命中段落在正文里的字位置
	text        []rune
	links       []string
	images      []RenderedImage
	previous    []RenderedPageSnapshot
	find        string
	findNote    string
}

func newBrowserRenderSequence(page RenderedPage, find string) browserRenderSequence {
	terms := browserRenderFindTerms(find)
	seq := browserRenderSequence{
		page:     page,
		text:     []rune(page.Text),
		links:    browserRenderLinks(page, terms),
		images:   page.Images,
		previous: page.PreviousPages,
		find:     find,
	}
	if len(terms) > 0 {
		seq.matches, seq.matchStarts = browserRenderFindMatches(page.Text, terms)
		if len(seq.matches) == 0 {
			seq.findNote = "整页正文里没找到 " + strings.Join(terms, "、") + "；页面没写不等于没有，可以从 links 打开相关页面接着找。"
		}
	}
	return seq
}

func (s browserRenderSequence) textStart() int  { return len(s.matches) }
func (s browserRenderSequence) linkStart() int  { return s.textStart() + len(s.text) }
func (s browserRenderSequence) imageStart() int { return s.linkStart() + len(s.links) }
func (s browserRenderSequence) prevStart() int  { return s.imageStart() + len(s.images) }
func (s browserRenderSequence) total() int      { return s.prevStart() + len(s.previous) }

func browserRenderOutput(page RenderedPage, find string, offset, budget int) (string, error) {
	return newBrowserRenderSequence(page, find).render(offset, budget)
}

// payload 是只装 [offset, end) 这一段时的输出。
func (s browserRenderSequence) payload(offset, end int) browserRenderPayload {
	page := s.page
	page.Text = ""
	page.Images = nil
	page.PreviousPages = nil
	out := browserRenderPayload{RenderedPage: page, FindNote: s.findNote, Offset: offset, TotalPositions: s.total()}
	clip := func(start, stop int) (int, int, bool) {
		lo, hi := max(offset, start), min(end, stop)
		return lo - start, hi - start, lo < hi
	}
	if lo, hi, ok := clip(0, s.textStart()); ok {
		out.FindMatches = s.matches[lo:hi]
		for _, start := range s.matchStarts[lo:hi] {
			out.FindOffsets = append(out.FindOffsets, s.textStart()+start)
		}
	}
	if lo, hi, ok := clip(s.textStart(), s.linkStart()); ok {
		out.Text = strings.TrimRight(string(s.text[lo:hi]), "\n")
	}
	if lo, hi, ok := clip(s.linkStart(), s.imageStart()); ok {
		out.Links = s.links[lo:hi]
	}
	if lo, hi, ok := clip(s.imageStart(), s.prevStart()); ok {
		out.Images = s.images[lo:hi]
	}
	if lo, hi, ok := clip(s.prevStart(), s.total()); ok {
		out.PreviousPages = s.previous[lo:hi]
	}
	// 正文没在这一页读完（或者 HTML 本身超过了下载上限）就算部分读取。
	out.Truncated = s.page.Truncated || (end < s.linkStart() && len(s.text) > 0)
	if end < s.total() {
		out.NextOffset = end
		out.ReadNotice = s.readNotice(end)
	}
	out.SourceStage = browserPayloadSourceStage(out)
	return out
}

func (s browserRenderSequence) readNotice(next int) string {
	var left []string
	if rest := len(s.text) - max(0, next-s.textStart()); rest > 0 {
		left = append(left, fmt.Sprintf("正文还有 %d 字", min(rest, len(s.text))))
	}
	for _, part := range []struct {
		name       string
		start, end int
	}{{"链接", s.linkStart(), s.imageStart()}, {"图片", s.imageStart(), s.prevStart()}, {"跳转前页面", s.prevStart(), s.total()}} {
		if rest := part.end - max(next, part.start); rest > 0 {
			left = append(left, fmt.Sprintf("%s还有 %d 条", part.name, rest))
		}
	}
	if next < s.textStart() {
		left = append([]string{fmt.Sprintf("查找命中还有 %d 段", s.textStart()-next)}, left...)
	}
	args := map[string]any{"url": firstNonEmptyString(s.page.RequestedURL, s.page.URL), "offset": next}
	if s.find != "" {
		args["find"] = s.find
	}
	call, _ := json.Marshal(args)
	return "这一页没给完：" + strings.Join(left, "，") + "。需要后面的内容就再调一次 browser_render，参数 " + string(call) + "（find 保持不变，offset 照抄）。没读到的部分不能当成页面没写。"
}

func (s browserRenderSequence) fits(offset, end, budget int) (string, bool, error) {
	data, err := json.MarshalIndent(s.payload(offset, end), "", "  ")
	if err != nil {
		return "", false, err
	}
	return string(data), utf8.RuneCount(data) <= budget, nil
}

// render 从 offset 起尽量多装：查找命中、链接、图片、跳转前页面按整条装；正文按整行
// 装，第一行就装不下时才从行内切。至少装一个位置，保证续读一定往前走。
func (s browserRenderSequence) render(offset, budget int) (string, error) {
	total := s.total()
	offset = min(max(offset, 0), total)
	if budget <= 0 {
		budget = ToolOutputMaxChars
	}
	if offset >= total {
		out, _, err := s.fits(offset, offset, budget)
		return out, err
	}
	var bounds []int
	for position := offset; position < total; {
		position = s.nextBoundary(position)
		bounds = append(bounds, position)
	}
	// 装得越多输出越长，二分找装得下的最后一条。
	lo, hi := -1, len(bounds)-1
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if _, ok, err := s.fits(offset, bounds[mid], budget); err != nil {
			return "", err
		} else if ok {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo >= 0 {
		out, _, err := s.fits(offset, bounds[lo], budget)
		return out, err
	}
	// 第一条就装不下：正文从行内切，取装得下的最长一段；别的条目只能整条给。
	end := bounds[0]
	if offset >= s.textStart() && offset < s.linkStart() {
		low, high := offset+1, end
		for low < high {
			mid := low + (high-low+1)/2
			if _, ok, err := s.fits(offset, mid, budget); err != nil {
				return "", err
			} else if ok {
				low = mid
			} else {
				high = mid - 1
			}
		}
		end = low
	}
	out, _, err := s.fits(offset, end, budget)
	return out, err
}

// nextBoundary 返回从 position 起下一条的结尾：正文是这一行的末尾（含换行），其余
// 每条占一个位置。
func (s browserRenderSequence) nextBoundary(position int) int {
	if position < s.textStart() || position >= s.linkStart() {
		return position + 1
	}
	index := position - s.textStart()
	for index < len(s.text) && s.text[index] != '\n' {
		index++
	}
	if index < len(s.text) {
		index++
	}
	return s.textStart() + index
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
		text := strings.Join(strings.Fields(link.Text), " ")
		candidates = append(candidates, candidate{line: text + " | " + key, rank: rank, order: index})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].order < candidates[j].order
	})
	lines := make([]string, len(candidates))
	for index, item := range candidates {
		lines[index] = item.line
	}
	return lines
}

// browserRenderFindMatches 在正文里找关键词，取命中所在的整行连同前后各
// browserRenderMatchContext 行；相邻的命中合并成一段。每段同时返回它在正文里的字位置。
func browserRenderFindMatches(text string, terms []string) ([]string, []int) {
	runes := []rune(text)
	lower := []rune(strings.ToLower(text))
	if len(lower) != len(runes) {
		// 大小写转换改变了长度（极少数字符），退回按原文匹配，下标才对得上。
		lower = runes
	}
	lineStart := func(index int) int {
		for index > 0 && runes[index-1] != '\n' {
			index--
		}
		return index
	}
	lineEnd := func(index int) int {
		for index < len(runes) && runes[index] != '\n' {
			index++
		}
		return index
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
			start, end := lineStart(index), lineEnd(index+len(needle))
			for range browserRenderMatchContext {
				if start > 0 {
					start = lineStart(start - 1)
				}
				if end < len(runes) {
					end = lineEnd(end + 1)
				}
			}
			spans = append(spans, span{start, end})
			index += len(needle) - 1
		}
	}
	if len(spans) == 0 {
		return nil, nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := []span{spans[0]}
	for _, item := range spans[1:] {
		last := &merged[len(merged)-1]
		if item.start <= last.end+1 {
			last.end = max(last.end, item.end)
			continue
		}
		merged = append(merged, item)
	}
	excerpts := make([]string, len(merged))
	starts := make([]int, len(merged))
	for index, item := range merged {
		excerpts[index] = strings.TrimSpace(string(runes[item.start:item.end]))
		starts[index] = item.start
	}
	return excerpts, starts
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

// pageByLines 从 offset 起按整行取 text 的一段，让 build(这一段, 下一段起点) 编出的
// JSON 不超过 budget；第一行就装不下才从行内切，下一页从切口接上。browser_text 和
// browser_eval 这类只有一段正文的结果都用它分页。
func pageByLines(text []rune, offset, budget int, build func(chunk string, end int) any) (string, error) {
	offset = min(max(offset, 0), len(text))
	if budget <= 0 {
		budget = ToolOutputMaxChars
	}
	encode := func(end int) (string, bool, error) {
		data, err := json.Marshal(build(string(text[offset:end]), end))
		return string(data), utf8.RuneCount(data) <= budget, err
	}
	var bounds []int
	for position := offset; position < len(text); {
		for position < len(text) && text[position] != '\n' {
			position++
		}
		if position < len(text) {
			position++
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
	end := offset
	if lo >= 0 {
		end = bounds[lo]
	} else {
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

// FirstTextPage 按整行取 text 开头不超过 limit 字的一段，第一行就超了才从行内切；返回
// 这一段和后面的起点（rune 位置，等于 browser_render 不带 find 时的 offset）。给不经过
// 工具、直接把网页放进上下文的地方用：放第一页，并照这个起点写明怎么续读。
func FirstTextPage(text string, limit int) (string, int) {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text, len(runes)
	}
	end := 0
	for end < len(runes) {
		next := end
		for next < len(runes) && runes[next] != '\n' {
			next++
		}
		if next < len(runes) {
			next++
		}
		if next > limit {
			break
		}
		end = next
	}
	if end == 0 {
		end = limit
	}
	return strings.TrimRight(string(runes[:end]), "\n"), end
}
