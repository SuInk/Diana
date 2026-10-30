// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// 回复过滤是主人兜底用的：提示词管不住的口癖（行尾「（接一段话」不闭合这类），
// 写一条正则直接从发出去的文字里删掉。它只删不改写，也不判断语义——要换说法
// 该去改回复口吻，不是在这里堆替换规则。
const (
	maxReplyFilterRules     = 50
	maxReplyFilterRuleRunes = 200
)

// 默认带一条规则，兜住漏进正文的内部控制标记（[[DIANA_REPLY_SINGLE]] 这类）。
// 开头结尾写对或写歪的标记发送层已经剥掉，这条管的是夹在正文中间、或者写歪到
// 发送层认不出的那些。它只是预填进列表，主人可以改也可以删。
func defaultReplyFilterRules() []string {
	return []string{`\[\[?\s*/?\s*[A-Z]{4,6}_(?:REPLY|LINES|REFUSE)_[A-Z]+\s*\]\]?`}
}

// cloneReplyFilterRules 保留 nil 和空列表的区别：空列表是主人清空了默认规则。
func cloneReplyFilterRules(rules []string) []string {
	if rules == nil {
		return nil
	}
	return append([]string{}, rules...)
}

var replyFilterCache sync.Map // pattern -> *regexp.Regexp

func compileReplyFilterRule(rule string) (*regexp.Regexp, error) {
	if cached, ok := replyFilterCache.Load(rule); ok {
		return cached.(*regexp.Regexp), nil
	}
	compiled, err := regexp.Compile(rule)
	if err != nil {
		return nil, err
	}
	replyFilterCache.Store(rule, compiled)
	return compiled, nil
}

func validateReplyFilterRules(rules []string) error {
	if len(rules) > maxReplyFilterRules {
		return fmt.Errorf("回复过滤规则最多 %d 条", maxReplyFilterRules)
	}
	for _, rule := range rules {
		if len([]rune(rule)) > maxReplyFilterRuleRunes {
			return fmt.Errorf("单条回复过滤规则不能超过 %d 字", maxReplyFilterRuleRunes)
		}
		compiled, err := compileReplyFilterRule(rule)
		if err != nil {
			return fmt.Errorf("回复过滤规则 %q 不是合法的正则：%v", rule, err)
		}
		// 能匹配空串的规则（比如 `x*`）删不掉任何东西，多半是写错了。
		if compiled.MatchString("") {
			return fmt.Errorf("回复过滤规则 %q 会匹配空文本，请检查是否漏写了内容", rule)
		}
	}
	return nil
}

// applyReplyFilters 把每条规则的匹配内容从回复里删掉。多条消息用换行分开，
// `$` 这类锚点要对每一行都起作用，所以统一按多行模式匹配。只删不改写：没被删到
// 的地方一个字符都不动，Markdown 的加粗、列表、链接和行尾硬换行空格都原样保留。
func applyReplyFilters(rules []string, text string) string {
	if len(rules) == 0 || text == "" {
		return text
	}
	compiled := make([]*regexp.Regexp, 0, len(rules))
	for _, rule := range rules {
		if re, err := compileReplyFilterRule("(?m)" + rule); err == nil {
			compiled = append(compiled, re)
		}
	}
	return runReplyFilters(compiled, text, nil)
}

// replyFilterProtected 是过滤规则不能碰的片段：代码块、行内代码，以及发送层要认的
// 分条、换行、引用、@ 标记和 CQ 码。规则匹配跨过它们时，它们的原字节照样留下。
// 漏进正文的 [[DIANA_…]] 控制标记不在其中，默认规则删的就是它们。
var replyFilterProtected = regexp.MustCompile("(?s:```.*?(?:```|$))|(?s:~~~.*?(?:~~~|$))|`[^`\n]+`|\\[(?:CQ:[^\\]\\n]+|diana-(?:msg|line|reply:[^\\]\\n]+|at:[^\\]\\n]+))\\]" +
	`|\[[ \t]*@?[ \t]*` + dianaMentionVariantCore + `[ \t]*\]|\[[ \t]*` + dianaReplyVariantCore + `[ \t]*\]`)

var replyFilterMessageBreak = regexp.MustCompile(`\[diana-(?:msg|line)\]`)

// runReplyFilters 按顺序跑规则；removed 不为空时报告每条规则删掉的片段。
// 分条标记两边是两条消息，`$` 要在标记前停住，不然一条里没闭合的括号会把下一条也删掉。
func runReplyFilters(rules []*regexp.Regexp, text string, removed func(rule int, fragment string)) string {
	var out strings.Builder
	changed, cursor := false, 0
	for _, marker := range append(replyFilterMessageBreak.FindAllStringIndex(text, -1), []int{len(text), len(text)}) {
		segment := text[cursor:marker[0]]
		for i, re := range rules {
			next := removeReplyFilterMatches(segment, re, func(fragment string) {
				if removed != nil {
					removed(i, fragment)
				}
			})
			changed = changed || next != segment
			segment = next
		}
		out.WriteString(segment)
		out.WriteString(text[marker[0]:marker[1]])
		cursor = marker[1]
	}
	if !changed {
		return text
	}
	return strings.TrimSpace(out.String())
}

func removeReplyFilterMatches(text string, pattern *regexp.Regexp, removed func(fragment string)) string {
	matches := pattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	protected := replyFilterProtected.FindAllStringIndex(text, -1)
	var out strings.Builder
	cursor, p := 0, 0
	for _, match := range matches {
		out.WriteString(text[cursor:match[0]])
		for p < len(protected) && protected[p][1] <= match[0] {
			p++
		}
		kept, from := false, match[0]
		for i := p; i < len(protected) && protected[i][0] < match[1]; i++ {
			start, end := max(match[0], protected[i][0]), min(match[1], protected[i][1])
			if from < start {
				removed(text[from:start])
			}
			out.WriteString(text[start:end])
			kept, from = true, end
		}
		if from < match[1] {
			removed(text[from:match[1]])
		}
		// 删到行尾时把删除点前面的空白一起收掉，别留下「先重启 」这种尾巴。
		if !kept && (match[1] == len(text) || text[match[1]] == '\n') {
			trimmed := strings.TrimRight(out.String(), " \t")
			out.Reset()
			out.WriteString(trimmed)
		}
		cursor = match[1]
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// ValidateReplyFilterRules 给 WebUI 的帮写和测试用，规则同保存时的校验。
func ValidateReplyFilterRules(rules []string) error {
	return validateReplyFilterRules(rules)
}

// ReplyFilterStep 是测试面板里一条规则的效果：删掉了哪些片段，或者规则本身不合法。
type ReplyFilterStep struct {
	Rule    string   `json:"rule"`
	Removed []string `json:"removed"`
	Error   string   `json:"error,omitempty"`
}

// PreviewReplyFilters 按发送时的顺序逐条跑规则，返回最终文本和每条删掉的内容。
// 不合法的规则和发送时一样跳过，只在结果里标出原因。
func PreviewReplyFilters(rules []string, text string) (string, []ReplyFilterStep) {
	steps := make([]ReplyFilterStep, 0, len(rules))
	compiled := make([]*regexp.Regexp, 0, len(rules))
	stepOf := make([]int, 0, len(rules))
	for _, rule := range rules {
		step := ReplyFilterStep{Rule: rule, Removed: []string{}}
		if err := validateReplyFilterRules([]string{rule}); err != nil {
			step.Error = err.Error()
		} else {
			re, _ := compileReplyFilterRule("(?m)" + rule)
			compiled = append(compiled, re)
			stepOf = append(stepOf, len(steps))
		}
		steps = append(steps, step)
	}
	result := runReplyFilters(compiled, text, func(rule int, fragment string) {
		step := &steps[stepOf[rule]]
		step.Removed = append(step.Removed, fragment)
	})
	return result, steps
}
