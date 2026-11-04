package assistant

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

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

// splitReplyBlocks 是纯文本平台没开「换行分条」时的切法：按块分条，而不是按行。
//
// 模型常在一条里写「你问的是 Ice 吧……<diana-br/>不过真别再翻这些了」：它想分开
// 说，却用了消息内换行。群里没人这样发消息，气泡中间断一行只显得奇怪；而且这条
// 进了历史，下一轮模型照着自己学，越写越多。所以普通的话之间的换行另起一条。
//
// 以前这里是「全拆或全不拆」：只要有一行像标签、链接或列表，整条原样留着，几段话
// 挤进一个长气泡；换个说法又被拆成几条。模型猜不到换行最后变成什么样，排版就显得
// 随机。现在逐块判断，一条消息里排过版的部分和普通的话各走各的：
//
//	普通的话                   一行一条
//	引导语 + 列表 / 代码块     连同引出它的那一行整块一条（同 splitReplyLinesKeepingLists）
//	小节标题                   连同它下面的正文一条，到下一个小节标题为止
//	单独一行的链接             跟着上一句，不单独成条
//	引用                       连续的引用行加上紧跟的那句评论一条
//
// 「短标签：内容」只有成串出现（或跟在引导语后面）才算清单；单独一行的「先回答你：……」
// 是一句话，照普通的话处理。
func splitReplyBlocks(text string) []string {
	masked, fences := maskFencedCodeBlocks(text)
	var lines []string
	for _, line := range strings.Split(masked, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, " \t"))
		}
	}
	const (
		modeNone = iota
		modeProse
		modeBlock
		modeSection
		modeQuote
	)
	var out, group []string
	mode := modeNone
	// looseItems：这个清单是引导语带出来的、没有项目符号的短条目，后面的短行也算它的。
	looseItems := false
	flush := func() {
		if len(group) > 0 {
			out = append(out, strings.Join(group, "\n"))
		}
		group, mode, looseItems = nil, modeNone, false
	}
	start := func(line string, next int) {
		flush()
		group, mode = []string{line}, next
	}
	leadsIn := func() bool {
		return mode == modeProse && len(group) == 1 && isReplyBlockLeadIn(group[0])
	}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		_, fenced := codeFenceIndex(trimmed)
		switch {
		case isDocumentSectionLabel(trimmed):
			start(line, modeSection)
		case mode == modeSection:
			group = append(group, line)
		case fenced:
			// 代码块前面那一句是在介绍它（「改成这样：」「我改了这里」），一起发。
			if mode != modeProse || len(group) != 1 {
				flush()
			}
			group = append(group, line)
			flush()
		case len(group) > 0 && isReplyLinkLine(trimmed):
			group = append(group, line)
		case strings.HasPrefix(trimmed, ">"):
			if mode != modeQuote {
				start(line, modeQuote)
			} else {
				group = append(group, line)
			}
		case mode == modeQuote:
			group = append(group, line)
			flush()
		case isReplyListBlockLine(lines, index):
			if mode == modeBlock || leadsIn() {
				group = append(group, line)
				mode = modeBlock
			} else {
				start(line, modeBlock)
			}
		case mode == modeBlock && line != strings.TrimLeft(line, " \t"):
			// 缩进续行：属于上一个列表项。
			group = append(group, line)
		case (leadsIn() || looseItems) && isReplyLooseItemLine(trimmed):
			// 「蓝标核心就几样：⏎权重置顶⏎发长推文」：没写项目符号的清单。
			group = append(group, trimmed)
			mode, looseItems = modeBlock, true
		case leadsIn():
			// 引导语后面不是清单，至少把紧跟的那句带上，冒号不单独挂成一条。
			group = append(group, trimmed)
			flush()
		default:
			start(trimmed, modeProse)
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

// splitPlainProseLines 是富文本平台（Telegram、QQ 官方 Markdown）没开「换行分条」时的
// 兜底：一条消息里只有几句普通的话、中间用换行隔开的，拆成几条发。
//
// 富文本平台的一条消息本来就是一篇排过版的 Markdown：空行分段、标题、表格都正常
// 渲染，所以只拆「每一行都是普通话」的那种。只要有一行是列表、编号、「标签：内容」、
// 引导语、小节标题、引用或链接，或者带代码、空行分段，整条原样留着。纯文本平台的
// 气泡渲染不了这些，走 splitReplyBlocks 按块切。
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

// isReplyLinkLine 认出基本只有一个链接的行：「https://…」「商店页：https://…」。
// 链接前面跟着一整句话的（「这两天回暖。来源：https://…」）是普通的话，自己成条。
func isReplyLinkLine(line string) bool {
	at := strings.Index(line, "://")
	if at < 0 {
		return false
	}
	// 去掉 scheme（https），剩下的就是链接前面的那段字。
	head := strings.TrimRightFunc(line[:at], func(r rune) bool { return r <= unicode.MaxASCII && unicode.IsLetter(r) })
	return utf8.RuneCountInString(head) <= structuredReplyLabelMaxRunes && !strings.ContainsAny(head, "。！？，,!?")
}

// isReplyLooseItemLine 认出引导语后面没写项目符号的短条目：一个短语，不是一句话。
func isReplyLooseItemLine(line string) bool {
	return utf8.RuneCountInString(line) <= replyLooseItemMaxRunes && !strings.ContainsAny(line, "，。！？；…,!?;") && !strings.Contains(line, "://")
}

// replyLooseItemMaxRunes 是无符号条目的长度上限；再长就是一句话了。
const replyLooseItemMaxRunes = 20

// isReplyListBlockLine 判断第 index 行是不是清单的一部分。项目符号、编号和表格行
// 本身就是；「短标签：内容」要有个伴——上一行或下一行也是清单行，或者上一行是引导语。
func isReplyListBlockLine(lines []string, index int) bool {
	line := strings.TrimSpace(lines[index])
	if isListReplyLine(line) {
		return true
	}
	if !isLabeledReplyLine(line) {
		return false
	}
	if index > 0 {
		previous := strings.TrimSpace(lines[index-1])
		if isStructuredReplyLine(previous) || isReplyBlockLeadIn(previous) {
			return true
		}
	}
	return index+1 < len(lines) && isStructuredReplyLine(strings.TrimSpace(lines[index+1]))
}

// replyLineSplitPrompt 告诉模型换行到底会不会另起一条。发送层和提示词必须对「换行
// 分不分条」给出同一个答案，所以按本轮实际生效的分条设置判断：开了换行分条是一行
// 一条；没开时走 splitPlainProseLines 的按块切，也要照实说，否则模型以为换行只是
// 气泡内排版，写出来的几句话却被拆开、或者因为某一行像标签整段挤成一条。
func replyLineSplitPrompt(limits chatSplitLimits) string {
	if limits.SingleMessage {
		return ""
	}
	if limits.LineSplit && !limits.MarkerOnly {
		return "当前开启换行分条：同一条消息里的每个 " + notificationLineMarker + " 都会拆成单独一条发出。列表、表格和代码块内部的换行不拆，整块连同引出它的那一行作为一条发送；想让几句话留在同一条里，就写在同一行，不要用空行分段。"
	}
	if limits.KeepProseLines || limits.LineBreakMode == replyLinesCompact {
		return ""
	}
	if limits.PreserveBlankLines {
		return "换行怎么发：一条消息里只有几句普通的话、用换行隔开时，会拆成几条发出；带列表、标签、链接、代码、引用或空行分段的，整条按排版发送。"
	}
	return "换行怎么发：两句普通的话之间换行，会拆成两条发出，想留在同一条就写在同一行。列表、表格、代码块连同引出它的那一行整块一条；小节标题和它下面的正文一条；单独一行的链接跟着上一句。"
}

// isReplyBlockLeadIn 认出引出列表或代码块的那一行。
func isReplyBlockLeadIn(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return strings.HasSuffix(line, "：") || strings.HasSuffix(line, ":") || isDocumentSectionLabel(line)
}
