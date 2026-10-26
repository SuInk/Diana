// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
)

var (
	qqMarkdownListItem = regexp.MustCompile(`^[ \t]*([-*+]|\d+[.)])[ \t]+`)
	qqMarkdownQuote    = regexp.MustCompile(`^[ \t]*>`)
)

// qqMarkdownLatex 认出带 LaTeX 命令的 $…$ 行内公式。只认里面有反斜杠命令的，
// 免得「$5 和 $10」这种价格被当成公式。
var qqMarkdownLatex = regexp.MustCompile(`\$\$|\$[^$\n]*\\[A-Za-z]+[^$\n]*\$`)

// qqOfficialMarkdownContent 在文本确实带 Markdown 或 LaTeX 公式时给出要发的
// markdown.content。
//
// 大白话照旧走纯文本：换成 Markdown 消息会改变气泡样式，却什么也没渲染。群友实测
// QQ 的原生 Markdown 能渲染 $…$ 和 $$…$$ 公式，官方文档没写，但只有公式的回复也
// 得走 Markdown 才看得到排版。
func qqOfficialMarkdownContent(text string) (string, bool) {
	if !platformTextHasMarkdown(text) && !qqMarkdownLatex.MatchString(text) {
		return "", false
	}
	return qqMarkdownLineBreaks(downgradeMarkdownFor(text, qqOfficialMarkdownFeatures)), true
}

// qqMarkdownLineBreaks 把相邻两行之间的单个换行补成空行。
//
// QQ 的 Markdown 按标准语义把单个换行当成段内软换行，几行话会被拼成一段；
// 列表前面紧贴正文时整个列表都不识别。所以正文行之间一律隔一个空行，只有连续
// 的列表项、连续的引用行、代码块和 $$ 公式块内部保持原样，否则列表会被拆散、
// 引用会断开、代码会被撑开。
func qqMarkdownLineBreaks(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)*2)
	inMath, inFence := false, false
	prev := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		opensFence := strings.HasPrefix(trimmed, "```")
		if !inMath && !inFence && prev != "" && trimmed != "" && !qqMarkdownLinesJoin(prev, line) {
			out = append(out, "")
		}
		out = append(out, line)
		if opensFence {
			inFence = !inFence
		} else if !inFence && strings.Count(trimmed, "$$")%2 == 1 {
			inMath = !inMath
		}
		if trimmed == "" {
			prev = ""
		} else {
			prev = line
		}
	}
	return strings.Join(out, "\n")
}

// qqMarkdownLinesJoin 判断两行是不是同一个块，不能在中间插空行。
func qqMarkdownLinesJoin(prev, line string) bool {
	if qqMarkdownListItem.MatchString(prev) || strings.HasPrefix(prev, "  ") || strings.HasPrefix(prev, "\t") {
		// 列表项后面跟列表项或缩进的续行，仍属于同一个列表。
		if qqMarkdownListItem.MatchString(line) || strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			return true
		}
	}
	return qqMarkdownQuote.MatchString(prev) && qqMarkdownQuote.MatchString(line)
}
