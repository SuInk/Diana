package assistant

import "strings"

// splitReplyLinesKeepingLists 是「换行分条」开关打开后的切法：一行一条消息。
//
// 列表除外。连续的项目符号、编号、表格行和「短标签：内容」行是一个整体，拆成一项
// 一条就没法对照着读了；项目下面缩进的续行跟着它的项目走。引出列表的那一行（以冒号
// 结尾，或是一个小节标题）也并进列表那条，不单独挂着一句「步骤如下：」。
// 代码围栏同理整块不拆，引出它的那一行一样并进去。
func splitReplyLinesKeepingLists(text string) []string {
	masked, fences := maskFencedCodeBlocks(text)
	var out, group []string
	inBlock := false
	flush := func() {
		if len(group) > 0 {
			out = append(out, strings.Join(group, "\n"))
		}
		group, inBlock = nil, false
	}
	// 当前这组只有一行引导语时，后面的列表或代码块并进来。
	leadsIn := func() bool {
		return !inBlock && len(group) == 1 && isReplyBlockLeadIn(group[0])
	}
	for _, line := range strings.Split(masked, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		line = strings.TrimRight(line, " \t")
		if _, fenced := codeFenceIndex(trimmed); fenced {
			if !leadsIn() {
				flush()
			}
			group = append(group, line)
			flush()
			continue
		}
		switch {
		case isStructuredReplyLine(trimmed):
			if !inBlock && !leadsIn() {
				flush()
			}
			group = append(group, line)
			inBlock = true
		case inBlock && line != trimmed:
			// 缩进续行：属于上一个列表项。
			group = append(group, line)
		default:
			flush()
			group = append(group, line)
		}
	}
	flush()
	for index := range out {
		for fence, block := range fences {
			out[index] = strings.ReplaceAll(out[index], codeFencePlaceholder(fence), block)
		}
	}
	return out
}

// splitPlainProseLines 是没开「换行分条」时的兜底：一条消息里只有几句普通的话、
// 中间用换行隔开的，拆成几条发。
//
// 模型常在一条里写「你问的是 Ice 吧……[diana-line]不过真别再翻这些了」：它想分开
// 说，却用了消息内换行。群里没人这样发消息，气泡中间断一行只显得奇怪；而且这条
// 进了历史，下一轮模型照着自己学，越写越多（线上一天约 8% 的回复这样断行，历史里
// 机器人的发言 11% 带换行）。
//
// 只拆「每一行都是普通话」的那种。只要有一行是列表、编号、「标签：内容」、引导语、
// 小节标题、引用或链接，或者带代码、空行分段，这条就是排过版的，整条原样留着。
func splitPlainProseLines(text string) []string {
	if strings.Contains(text, "```") {
		return []string{text}
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return []string{text}
		}
		if line != strings.TrimLeft(line, " \t") || !isPlainProseReplyLine(trimmed) {
			return []string{text}
		}
		lines = append(lines, trimmed)
	}
	if len(lines) < 2 {
		return []string{text}
	}
	return lines
}

func isPlainProseReplyLine(line string) bool {
	if isStructuredReplyLine(line) || isReplyBlockLeadIn(line) {
		return false
	}
	if strings.HasPrefix(line, ">") || strings.Contains(line, "://") {
		return false
	}
	return true
}

// replyLineSplitPrompt 告诉模型换行会另起一条。发送层和提示词必须对「换行分不分条」
// 给出同一个答案，所以按本轮实际生效的分条设置判断。
func replyLineSplitPrompt(limits chatSplitLimits) string {
	if !limits.LineSplit || limits.SingleMessage || limits.MarkerOnly {
		return ""
	}
	return "当前开启换行分条：同一条消息里的每个 " + notificationLineMarker + " 都会拆成单独一条发出。列表、表格和代码块内部的换行不拆，整块连同引出它的那一行作为一条发送；想让两句留在同一条里，就写在同一行。"
}

// isReplyBlockLeadIn 认出引出列表或代码块的那一行。
func isReplyBlockLeadIn(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return strings.HasSuffix(line, "：") || strings.HasSuffix(line, ":") || isDocumentSectionLabel(line)
}
