// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"regexp"
	"strings"
)

// 分条正则和回复过滤正则是一对：过滤是命中就删，这里是命中就另起一条消息。
//
// 起因是句尾挂半个「（」的群：模型学着用，却拿它当两件事之间的分隔符——「10个月
// 7990个commit这还菜？（ 分发卡壳先别怪自己…」，接梗和安慰硬接成一条；不带空格的
// 「主句（不过…」也是同一回事。提示词两头
// 都说过（「（」后面不能再有文字；想分开说就换行、别用空格硬接），11-09 加了后一句，
// 11-11 照样出现。光靠提示词挡不住，就在发送层按规则拆，规则写成正则放在 WebUI 里，
// 主人看得见、改得了。
//
// 规则带捕获组时只把第一个捕获组那一段换成分条，组外的字留在原处；不带捕获组就把
// 整段命中换成分条。只在会按内容分条的路径上生效，一次发完和原样转发不拆。

const (
	maxReplySplitRules     = 20
	maxReplySplitRuleRunes = 200
)

// defaultReplySplitRules：「（」后面（不管有没有空格）还有至少两个字、这一行又没有
// 「）」收口时，在「（」后面另起一条。括号注释会收口，不收口的基本都是「主句（不过…」
// 这种另起一件事：11-11 前 7 天生产回复里 125 条命中，最短的后半句也有五个字
// （「（本地宝写的」「（嫌吵就调低」）。后面只剩一个字的不拆，「（x」是语气尾巴，
// 拆开会单独发一条「x」。
func defaultReplySplitRules() []string {
	return []string{
		`（([ \t\x{3000}]*)[^）\n]{2,}$`,
	}
}

func validateReplySplitRules(rules []string) error {
	if len(rules) > maxReplySplitRules {
		return fmt.Errorf("分条正则最多 %d 条", maxReplySplitRules)
	}
	for _, rule := range rules {
		if len([]rune(rule)) > maxReplySplitRuleRunes {
			return fmt.Errorf("单条分条正则不能超过 %d 字", maxReplySplitRuleRunes)
		}
		compiled, err := compileReplyFilterRule(rule)
		if err != nil {
			return fmt.Errorf("分条正则 %q 不是合法的正则：%v", rule, err)
		}
		if compiled.MatchString("") {
			return fmt.Errorf("分条正则 %q 会匹配空文本，请检查是否漏写了内容", rule)
		}
	}
	return nil
}

// applyReplySplitRules 在命中处插入 <diana-message/>。和过滤一样按行匹配，已有的
// 分条、换行标记把正文切成几段分别跑；代码块、行内代码和各类标记不会被拆开。
func applyReplySplitRules(rules []string, text string) string {
	if len(rules) == 0 || text == "" {
		return text
	}
	compiled := make([]*regexp.Regexp, 0, len(rules))
	for _, rule := range rules {
		if re, err := compileReplyFilterRule("(?m)" + rule); err == nil {
			compiled = append(compiled, re)
		}
	}
	if len(compiled) == 0 {
		return text
	}
	var out strings.Builder
	cursor := 0
	for _, marker := range append(replyFilterMessageBreak.FindAllStringIndex(text, -1), []int{len(text), len(text)}) {
		segment := text[cursor:marker[0]]
		for _, re := range compiled {
			segment = splitAtReplyRule(segment, re)
		}
		out.WriteString(segment)
		out.WriteString(text[marker[0]:marker[1]])
		cursor = marker[1]
	}
	return out.String()
}

// splitAtReplyRule 逐个命中往后找：每拆一处，从分条点之后重新匹配，`$` 这类锚点对
// 剩下的部分照样成立，「会吗（ 我觉得会（ 反正试试」能拆成三条。
func splitAtReplyRule(text string, re *regexp.Regexp) string {
	var out strings.Builder
	rest := text
	for rest != "" {
		start, end, ok := nextReplySplit(rest, re)
		if !ok {
			break
		}
		out.WriteString(strings.TrimRight(rest[:start], " \t　"))
		out.WriteString(notificationSplitMarker)
		rest = strings.TrimLeft(rest[end:], " \t　")
	}
	out.WriteString(rest)
	return out.String()
}

// nextReplySplit 找第一处不落在受保护片段里的命中，返回要换成分条的那一段。
func nextReplySplit(text string, re *regexp.Regexp) (int, int, bool) {
	protected := replyFilterProtected.FindAllStringIndex(text, -1)
	for offset := 0; offset < len(text); {
		match := re.FindStringSubmatchIndex(text[offset:])
		if match == nil {
			return 0, 0, false
		}
		start, end := match[0], match[1]
		if len(match) >= 4 && match[2] >= 0 {
			start, end = match[2], match[3]
		}
		start, end = start+offset, end+offset
		// 拆在开头或结尾等于没拆，也防止空捕获组原地打转。
		if start > 0 && end < len(text) && !overlapsReplyProtected(protected, start, end) {
			return start, end, true
		}
		offset += max(match[1], match[0]+1)
	}
	return 0, 0, false
}

func overlapsReplyProtected(protected [][]int, start, end int) bool {
	for _, span := range protected {
		if span[0] < end && start < span[1] || span[0] <= start && start < span[1] {
			return true
		}
	}
	return false
}
