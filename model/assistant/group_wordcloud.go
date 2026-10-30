// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// 群聊词云：一段时间内群里说得最多的词。
//
// 和关系图一样不建新表，直接数 message_events 的 text。分词复用检索那套 gse
// 词典：词典开着就按词切；没开（默认不开，常驻要一百多 MB）就退回启发式——
// 短的中文连续段整段当一个词，长段切二字组。启发式会多出些碎片，但「哈哈哈」
// 「草」「下班」这类群聊高频词本来就短，词云的头部基本不受影响。

// GroupWordCloudWord 是词云里的一个词。
type GroupWordCloudWord struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

// GroupWordCloud 是一个群在某段时间内的词频统计。
type GroupWordCloud struct {
	GroupID  string     `json:"group_id"`
	Since    *time.Time `json:"since,omitempty"`
	Messages int        `json:"messages"`
	// Speakers 是参与统计的发言人数（不含机器人自己）。
	Speakers int                  `json:"speakers"`
	Words    []GroupWordCloudWord `json:"words"`
	// Segmenter 说明这次用的切词方式：dict 是词典分词，ngram 是启发式兜底。
	Segmenter string `json:"segmenter"`
	// Truncated 说明扫描撞到了条数上限，只反映最近这一段。
	Truncated bool `json:"truncated"`
}

// GroupWordCloudMaxWords 是返回的词数上限：再多词云就只剩一片看不清的小字。
const GroupWordCloudMaxWords = 120

var (
	wordCloudCQCode  = regexp.MustCompile(`\[CQ:[^\]]*\]`)
	wordCloudURL     = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	wordCloudBracket = regexp.MustCompile(`\[[^\[\]]{1,12}\]`)
	wordCloudMention = regexp.MustCompile(`@\S+`)
)

// wordCloudStopwords 是统计时丢掉的虚词和群聊口头填充。只收「出现得多但看了
// 也不知道群里在聊什么」的词；「哈哈」「草」这种本身就是群聊气质的词留着。
var wordCloudStopwords = func() map[string]struct{} {
	words := strings.Fields(`
的 了 是 我 你 他 她 它 们 在 有 和 就 不 也 都 这 那 啊 吧 吗 呢 呀 哦 嗯 噢 哎 诶 么 个 着 过 还 又 要 会 能 说 去 来 被 把 给 让 跟 与 及 或 而 但 很 太 真 好 没 才
我们 你们 他们 她们 它们 自己 这个 那个 这样 那样 这么 那么 什么 怎么 为什么 为啥 因为 所以 但是 然后 如果 虽然 可以 可能 就是 还是 不是 没有 一个 一下 一样 已经 现在 时候 知道 觉得 感觉 应该 问题 东西 有点 有些 的话 真的 其实 而且 或者 只是 只有 不过 还有 这种 那种 这些 那些 怎么样 多少 哪里 哪个 一点 一些 起来 出来 不会 不要 不能 不用 不知道 是不是 有没有 要不 这里 那里 之前 之后 以后 以前 今天 刚才 别人 大家 一直 直接 然后 好像 确实 反正 毕竟 估计 似乎
图片 表情 动画表情 视频 语音 文件 回复 撤回 消息 引用
the a an and or of to in on at for is are was were be been it this that with as by from i you he she we they me my your not no yes ok
`)
	set := make(map[string]struct{}, len(words))
	for _, word := range words {
		set[word] = struct{}{}
	}
	return set
}()

// CleanWordCloudText 去掉统计时不该算进去的部分：CQ 码、链接、[图片] 这类占位、@ 某人。
func CleanWordCloudText(text string) string {
	text = wordCloudCQCode.ReplaceAllString(text, " ")
	text = wordCloudURL.ReplaceAllString(text, " ")
	text = wordCloudBracket.ReplaceAllString(text, " ")
	text = wordCloudMention.ReplaceAllString(text, " ")
	return text
}

// WordCloudCounter 累计一批消息的词频。
type WordCloudCounter struct {
	counts map[string]int
	dict   bool
}

func NewWordCloudCounter() *WordCloudCounter {
	return &WordCloudCounter{counts: map[string]int{}, dict: cjkSegmentEnabled.Load() && cjkSegmentReady()}
}

// Segmenter 报告这次统计用的切词方式。
func (c *WordCloudCounter) Segmenter() string {
	if c.dict {
		return "dict"
	}
	return "ngram"
}

// Add 统计一条消息。同一条消息里重复刷的词只算一次：一个人一句话刷二十遍
// 「哈」，不该把整张词云压成一个字。
func (c *WordCloudCounter) Add(text string) {
	seen := map[string]struct{}{}
	for _, word := range c.words(CleanWordCloudText(text)) {
		if _, dup := seen[word]; dup {
			continue
		}
		seen[word] = struct{}{}
		c.counts[word]++
	}
}

// Top 返回出现次数最多的 limit 个词。只出现过一次的词不要：它们是噪声，也让
// 消息少的群词云看着像随机字。
func (c *WordCloudCounter) Top(limit int) []GroupWordCloudWord {
	words := make([]GroupWordCloudWord, 0, len(c.counts))
	for text, count := range c.counts {
		if count < 2 {
			continue
		}
		words = append(words, GroupWordCloudWord{Text: text, Count: count})
	}
	sort.Slice(words, func(left, right int) bool {
		if words[left].Count == words[right].Count {
			return words[left].Text < words[right].Text
		}
		return words[left].Count > words[right].Count
	})
	if limit > 0 && len(words) > limit {
		words = words[:limit]
	}
	return words
}

func (c *WordCloudCounter) words(text string) []string {
	var out []string
	var run []rune
	var latin []rune
	flushRun := func() {
		if len(run) > 0 {
			out = append(out, c.cjkWords(string(run))...)
			run = run[:0]
		}
	}
	flushLatin := func() {
		if len(latin) >= 2 {
			word := strings.ToLower(string(latin))
			if !isWordCloudStopword(word) && !isAllDigits(word) {
				out = append(out, word)
			}
		}
		latin = latin[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushLatin()
			run = append(run, r)
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			flushRun()
			latin = append(latin, r)
		default:
			flushRun()
			flushLatin()
		}
	}
	flushRun()
	flushLatin()
	return out
}

func (c *WordCloudCounter) cjkWords(run string) []string {
	var candidates []string
	if c.dict {
		// Cut 而不是 CutSearch：搜索模式会把「下班了」再拆出子词，同一处被数好几次。
		for _, word := range cjkSegmenter.Cut(run, true) {
			if word = strings.TrimSpace(word); word != "" {
				candidates = append(candidates, word)
			}
		}
	} else {
		candidates = heuristicCJKWords(run)
	}
	out := candidates[:0]
	for _, word := range candidates {
		// 「哈哈哈哈哈」长短不一，统一收成三个字，否则同一种笑会散成好几个词。
		if runes := []rune(word); len(runes) > 3 && isRepeatedSingleRune(word) {
			word = string(runes[:3])
		}
		if isWordCloudStopword(word) {
			continue
		}
		// 单字只留叠字以外的独立短句，例如「草」「6」这种整句回复；切出来的单字是碎片。
		if len([]rune(word)) < 2 && len([]rune(run)) > 1 {
			continue
		}
		out = append(out, word)
	}
	return out
}

// heuristicCJKWords 是没有词典时的切法：先在停用字处断开，两个字以内的段和
// 「哈哈哈」这种叠字整段算一个词，更长的切二字组。二字组会带出「晚火」这种跨词
// 碎片，但它们各自分散，计数压不过真正反复出现的词。
func heuristicCJKWords(run string) []string {
	var out []string
	var segment []rune
	flush := func() {
		switch {
		case len(segment) == 0:
		case len(segment) <= 2 || isRepeatedSingleRune(string(segment)):
			out = append(out, string(segment))
		default:
			for index := 0; index+1 < len(segment); index++ {
				out = append(out, string(segment[index:index+2]))
			}
		}
		segment = segment[:0]
	}
	for _, r := range run {
		if _, stop := wordCloudStopwords[string(r)]; stop {
			flush()
			continue
		}
		segment = append(segment, r)
	}
	flush()
	return out
}

func isWordCloudStopword(word string) bool {
	_, stop := wordCloudStopwords[strings.ToLower(word)]
	return stop || strings.TrimSpace(word) == ""
}

func isAllDigits(word string) bool {
	for _, r := range word {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isRepeatedSingleRune(word string) bool {
	runes := []rune(word)
	for _, r := range runes[1:] {
		if r != runes[0] {
			return false
		}
	}
	return len(runes) > 1
}
