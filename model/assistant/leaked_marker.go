// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
	"unicode"
)

// 模型把控制标记写成什么样没法穷举：<diana-single/> 少一层括号、
// diana-reply 丢了前缀写成 [reply:...]，每次都是线上漏出来才补一种写法。所以
// 发送前最后再按「长得像标记」清一遍，不管它原本想表达什么：
//
//   - 双层方括号 [[任何东西]]：正文里几乎不会有人这么写。
//   - [英文键:值]，值里没有空白：[reply:...]、[foo-id=123] 这类。值里带空格的
//     「[Note: 记得带伞]」是正文，不动。
//   - [全大写_下划线]：[DIANA_REPLY_SINGLE]、[REFUSE_CURRENT] 这类。
//
// 2026-10-30 回看生产 30 天的消息：机器人自己漏出去的有 [DIANA_REPLY_SINGLE]、
// [[/DIANA_REPLY_SINGLE]]、[DAIAN_REPLY_SINGLE] 这几种，群友贴进来的还有 [reply:...]，都在
// 这三类里。[agent_finalize:正文…] 也漏过，括号里是正文，只去外壳，见 unwrapWrappedReply。
//
// 标签写法 <diana-…>：@、引用、分条、换行留给后面消费，其余认不出的整个删掉；
// 不带 diana- 前缀的尖括号（群友贴的 HTML、泛型）不动。
//
// 全角【】同样处理。这些放过：网址和 Markdown 链接；正规的 <diana-at id="ID"/>（要留给
// 后面翻译成平台的 @）；OneBot 的 [CQ:…] 段；脱敏占位符 [REDACTED_*]（给人看的）；
// 代码块和行内代码。它只兜底，能解析的标记应该在前面就被消费掉。
var leakedControlMarkerPattern = regexp.MustCompile(mentionCodeSpanAlternation +
	`\[\[[^\[\]\n]{1,200}\]\]` +
	`|[\[【][ \t]*/?[ \t]*([A-Za-z][A-Za-z0-9_-]{0,40})[ \t]*[:：=][ \t]*[^\]】\s]{0,200}[ \t]*[\]】]` +
	`|[\[【][ \t]*/?[ \t]*[A-Z][A-Z0-9]*_[A-Z0-9_]+[ \t]*[\]】]` +
	// 标签写法：只认 diana- 打头的，群友贴的 HTML、泛型这类尖括号一律不碰。
	`|<[ \t]*/?[ \t]*diana-[a-z-]+[^<>\n]{0,200}>`)

// 整条回复被包成「[agent_finalize:正文]」时，外壳去掉、正文照发。只认整条最开头
// 的英文键，结尾的右括号可有可无（线上那条就没写完）。
var wrappedReplyPattern = regexp.MustCompile(`(?s)^[ \t\r\n]*[\[【][ \t]*([A-Za-z][A-Za-z0-9_-]{0,40})[ \t]*[:：=][ \t]*(.*?)[ \t\r\n]*[\]】]?[ \t\r\n]*$`)

func unwrapWrappedReply(text string) string {
	match := wrappedReplyPattern.FindStringSubmatch(text)
	if match == nil || strings.ContainsAny(match[2], "[]【】") || !containsHan(match[2]) {
		return text
	}
	switch strings.ToLower(match[1]) {
	case "cq", "diana-at", "diana-reply", "http", "https", "ftp", "mailto":
		return text
	}
	return match[2]
}

func containsHan(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func isConsumableDianaTag(token string) bool {
	return strings.HasPrefix(token, dianaMentionMarkerPrefix) || strings.HasPrefix(token, replyMarkerPrefix) ||
		token == notificationSplitMarker || token == notificationLineMarker
}

func stripLeakedControlMarkers(text string) string {
	if !strings.ContainsAny(text, "[【<") {
		return text
	}
	text = unwrapWrappedReply(text)
	var builder strings.Builder
	last := 0
	for _, bounds := range leakedControlMarkerPattern.FindAllStringSubmatchIndex(text, -1) {
		token := text[bounds[0]:bounds[1]]
		if isMentionCodeSpan(token) {
			continue
		}
		// 标准的 @、引用、分条、换行标签留给后面的发送步骤消费。
		if isConsumableDianaTag(token) {
			continue
		}
		// 脱敏占位符（[REDACTED_URL] 这类）是故意给人看的，告诉对方这里有东西被遮了。
		if strings.Contains(token, "REDACTED") {
			continue
		}
		// [https://…](…) 这种拿网址当链接文字的 Markdown 链接，线上真发过。
		if bounds[1] < len(text) && text[bounds[1]] == '(' {
			continue
		}
		// 值里有汉字说明括号里装的是正文，删了就丢话；ID 和标记参数都不会有汉字。
		// 包住整条回复的已经在 unwrapWrappedReply 去过外壳。
		if bounds[2] >= 0 && containsHan(token) {
			continue
		}
		if bounds[2] >= 0 {
			switch strings.ToLower(text[bounds[2]:bounds[3]]) {
			case "cq", "diana-at", "http", "https", "ftp", "mailto":
				continue
			}
		}
		// 标记两边的空格只留一个：「fufu [MSG_ID:1]』」删完是「fufu』」。
		before := text[last:bounds[0]]
		last = bounds[1]
		if last < len(text) && text[last] == ' ' {
			last++
		} else {
			before = strings.TrimSuffix(before, " ")
		}
		builder.WriteString(before)
	}
	if last == 0 {
		return text
	}
	builder.WriteString(text[last:])
	return strings.TrimSpace(builder.String())
}
