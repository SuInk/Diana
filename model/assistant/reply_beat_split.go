// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
	"unicode"
)

// 群友风格下，换行是分条信号。
//
// 这个风格的提示词一直在说「一条消息只讲一件事；句子短」，模型也照做了——它把两
// 句短的分成两行写。但投递侧只认 <dianabr> 和长度上限，两行短句于是挤在同一个气泡
// 里发出去，看起来还是「机器人一次说完一大段」，风格白设了。
//
// 之所以敢在这里给换行赋予语义，是因为群友风格的提示词里明确写了这件事（见
// stylePrompt）：两边对同一个符号的理解是一致的，不是运行时单方面猜。其余风格和
// 通知维持原样——那里换行只是排版。
//
// 触发条件卡得很紧，只认「连着几句短的」这一种形态：
//
//   - 至多 chatBeatMaxLines 行。行数一多就不是随口连发，是被压平的清单或段落。
//   - 每行都不超过 chatBeatMaxRunes 个字符。只要有一行长，整块就是正常的多行文本，
//     拆开只会把一段话截断。
//   - 没有任何一行带清单/步骤标记，也没有代码块。榜单和步骤本来就该待在一条里。
//
// 任何一条不满足就整块原样发出，不做部分拆分——半拆的结果比不拆更难读。
const (
	chatBeatMaxLines = 4
	chatBeatMaxRunes = 60
)

// chatBeatListMarker 匹配行首的清单与步骤标记：-、*、•、1. 、1)、一、、第 2 步。
var chatBeatListMarker = regexp.MustCompile(`^\s*([-*•·>]\s|\d+\s*[.)、．]|[一二三四五六七八九十]+\s*[、.)]|第\s*[一二三四五六七八九十\d]+\s*[步条点项])`)

// splitChatReply 在 splitReply 的基础上，按风格追加聊天体量的分条。
// 通知不走这里，它只关心「别把一张卡片切开」。
func splitChatReply(reply string, chunkSize int, style ReplyStyle) []string {
	chunks := splitReply(reply, chunkSize)
	if style.Normalized() != ReplyStyleGroupmate {
		return chunks
	}
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, splitShortBeats(chunk)...)
	}
	return out
}

// splitShortBeats 把「连着几句短的」拆成一句一条；不符合这个形态就原样返回。
func splitShortBeats(chunk string) []string {
	if !strings.Contains(chunk, "\n") || strings.Contains(chunk, "```") {
		return []string{chunk}
	}
	lines := strings.Split(chunk, "\n")
	beats := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		beats = append(beats, line)
	}
	if len(beats) < 2 || len(beats) > chatBeatMaxLines {
		return []string{chunk}
	}
	for _, beat := range beats {
		if len([]rune(beat)) > chatBeatMaxRunes {
			return []string{chunk}
		}
		if chatBeatListMarker.MatchString(beat) {
			return []string{chunk}
		}
		if isIndentedCodeLine(beat) {
			return []string{chunk}
		}
	}
	return beats
}

// isIndentedCodeLine 认出被当成代码贴进来的行。缩进在 TrimSpace 之后已经没了，
// 这里看的是内容形态：整行没有空格分隔的自然语言，却带着代码常见的符号。
func isIndentedCodeLine(line string) bool {
	if strings.HasPrefix(line, "$ ") || strings.HasPrefix(line, "# ") {
		return true
	}
	if strings.ContainsAny(line, "{};") && !strings.ContainsFunc(line, func(r rune) bool {
		return unicode.Is(unicode.Han, r)
	}) {
		return true
	}
	return false
}
