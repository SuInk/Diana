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
