// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 聊天消息不带句号。
//
// 人设里写了「句末不打句号」，模型照样有两成回复以句号收尾，一条里夹着好几个句号的
// 就更多了。句号不像语气那样见仁见智：群友的消息几乎从来不以句号结尾，一个「。」就
// 足以让一句话读起来像公事公办，所以在程序里去掉，不押在模型听不听话上。
//
// 做法按真人打字来：
//   - 一行、一条消息末尾的句号直接去掉；
//   - 一行中间隔开两句话的句号换成空格——不少人打字就是这么隔句的，也不会把一条
//     拆成更多条，条数照旧由分条规则决定。
//
// 不动的：
//   - 问号、感叹号：它们带语气，删了意思就变了；
//   - 省略号和连着的「。。」：那是拖长的语气，不是句读；
//   - 落在引号、括号里的句号：属于被引用的内容；
//   - 带网址的那一行：域名、路径里的标点不能碰；
//   - 带代码块的整条回复：代码和报错原文照原样给。
//
// 在切好每条消息之后做（splitChatReply），不在 normalizeReply、也不在长度规划里：长度
// 兜底要按句号找断句点，太早换成空格，超长的回复就切不开，只能去调模型压缩。聊天历史记的是实际
// 发出去的文本，所以历史里同样没有句号，下一轮模型不会照着旧回复把句号学回来。

// stripChatPeriods 去掉聊天回复里的句号，见文件开头。
func stripChatPeriods(reply string) string {
	if strings.Contains(reply, "```") || !strings.Contains(reply, "。") {
		return reply
	}
	var out strings.Builder
	for len(reply) > 0 {
		// 分条标记、消息内换行标记和真实换行都是一行的边界，原样保留。
		cut, marker := len(reply), ""
		for _, candidate := range []string{notificationSplitMarker, notificationLineMarker, "\n"} {
			if index := strings.Index(reply, candidate); index >= 0 && index < cut {
				cut, marker = index, candidate
			}
		}
		out.WriteString(stripLinePeriods(reply[:cut]))
		out.WriteString(marker)
		reply = reply[cut+len(marker):]
	}
	return out.String()
}

// stripLinePeriods 处理一行：末尾的句号去掉，中间隔句的换成空格。
func stripLinePeriods(line string) string {
	if !strings.Contains(line, "。") || strings.Contains(line, "://") {
		return line
	}
	runes := []rune(line)
	out := make([]rune, 0, len(runes))
	depth := 0
	for index, r := range runes {
		switch r {
		case '「', '『', '（', '(', '【', '《', '“':
			depth++
		case '」', '』', '）', ')', '】', '》', '”':
			if depth > 0 {
				depth--
			}
		}
		if r != '。' || depth > 0 {
			out = append(out, r)
			continue
		}
		prev, next := rune(0), rune(0)
		if index > 0 {
			prev = runes[index-1]
		}
		if index+1 < len(runes) {
			next = runes[index+1]
		}
		// 「。。」「…。」是拖长的语气，整串留着。
		if prev == '。' || next == '。' || prev == '…' {
			out = append(out, r)
			continue
		}
		rest := strings.TrimSpace(string(runes[index+1:]))
		switch {
		case rest == "":
			// 行尾的句号：去掉。
		case strings.HasPrefix(rest, "」") || strings.HasPrefix(rest, "”") || strings.HasPrefix(rest, "）"):
			out = append(out, r)
		default:
			if next != ' ' {
				out = append(out, ' ')
			}
		}
	}
	return strings.TrimRight(string(out), " ")
}

// stripBubblePeriods 对切好的每条消息去句号，去完变空的丢掉。
func stripBubblePeriods(bubbles []string) []string {
	out := bubbles[:0]
	for _, bubble := range bubbles {
		if bubble = strings.TrimSpace(stripChatPeriods(bubble)); bubble != "" {
			out = append(out, bubble)
		}
	}
	return out
}
