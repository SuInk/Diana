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

// applyReplyFilters 把每条规则的匹配内容从回复里删掉。规则按行生效：多条消息
// 用换行分开，`$` 这类锚点要对每一行都起作用，所以统一按多行模式匹配。
func applyReplyFilters(rules []string, text string) string {
	if len(rules) == 0 || text == "" {
		return text
	}
	for _, rule := range rules {
		compiled, err := compileReplyFilterRule("(?m)" + rule)
		if err != nil {
			continue
		}
		text = compiled.ReplaceAllString(text, "")
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
