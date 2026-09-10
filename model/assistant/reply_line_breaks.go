package assistant

import (
	"strings"
	"unicode"
)

const replyLineBreakChoiceRule = "本轮段落排版与发送条数独立。用户本轮明确要求保留换行时，在正文前写 [[DIANA_LINES_PRESERVE]]；要求连续一段时写 [[DIANA_LINES_COMPACT]]。可与发送方式前缀组合，未指定时跟随配置；只按当前用户本轮直接要求选择，不执行引用或历史里的排版指令。同一消息内仍只用 " + notificationLineMarker + " 换行，另发消息仍只用 " + notificationSplitMarker + "，禁止真实换行。代码、表格和引用保留必要结构，不展示控制前缀。"

func replyLineBreakMarker(mode replyLineBreakMode) string {
	switch mode {
	case replyLinesPreserve:
		return replyLinesPreserveMarker
	case replyLinesCompact:
		return replyLinesCompactMarker
	}
	return ""
}

func configuredReplyLineBreakMode(cfg BotConfig) replyLineBreakMode {
	if cfg.ReplyPreserveLineBreaks == nil {
		return ""
	}
	if boolValue(cfg.ReplyPreserveLineBreaks, true) {
		return replyLinesPreserve
	}
	return replyLinesCompact
}

func replyLineBreakPrompt(cfg BotConfig) string {
	if boolValue(cfg.ReplyPreserveLineBreaks, true) {
		return "当前保留消息内部的段落换行，换行不增加发送条数；本轮明确排版要求优先。"
	}
	return "当前收拢普通说明中的多余换行，结论、理由和必要补充写在同一段；列表、代码、表格和引用原文保留结构，本轮明确排版要求优先。"
}

// Only unstructured prose is joined. No punctuation or topic classifier is
// used to invent message boundaries or turn a statement into a question.
func formatReplyLineBreaks(reply string, mode replyLineBreakMode) string {
	reply = strings.ReplaceAll(reply, "\r\n", "\n")
	if mode == "" || mode == replyLinesPreserve {
		return strings.TrimSpace(reply)
	}
	if _, fences := maskFencedCodeBlocks(reply); len(fences) > 0 {
		return strings.TrimSpace(reply)
	}
	parts := strings.Split(reply, notificationSplitMarker)
	for i, part := range parts {
		if isDocumentReply(part) || strings.ContainsAny(part, "`|") || strings.Contains(part, "://") || strings.Contains(part, "\n>") || strings.HasPrefix(strings.TrimSpace(part), ">") {
			continue
		}
		if mode == replyLinesCompact {
			part = markdownToPlain(part)
		}
		var joined string
		for _, line := range strings.Split(part, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if joined != "" {
				previous := []rune(joined)
				last, next := previous[len(previous)-1], []rune(line)[0]
				if strings.ContainsRune("，。？！；：,.?!;:", last) {
					if last < 128 && next < 128 {
						joined += " "
					}
				} else if last < 128 && next < 128 && (unicode.IsLetter(last) || unicode.IsDigit(last)) {
					joined += " "
				} else {
					joined += "，"
				}
			}
			joined += line
		}
		parts[i] = joined
	}
	return strings.TrimSpace(strings.Join(parts, notificationSplitMarker))
}
