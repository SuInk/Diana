package assistant

import (
	"regexp"
	"strings"
)

type replyDeliveryMode string
type replyLineBreakMode string

const (
	replyDeliverySingle      replyDeliveryMode  = "single"
	replyDeliveryAuto        replyDeliveryMode  = "auto"
	replySingleMarker                           = "[[DIANA_REPLY_SINGLE]]"
	replyAutoMarker                             = "[[DIANA_REPLY_AUTO]]"
	replyLinesPreserve       replyLineBreakMode = "preserve"
	replyLinesCompact        replyLineBreakMode = "compact"
	replyLinesPreserveMarker                    = "[[DIANA_LINES_PRESERVE]]"
	replyLinesCompactMarker                     = "[[DIANA_LINES_COMPACT]]"
)

const replyDeliveryChoiceRule = "本轮用户的发送方式要求优先于上述默认分条和长文分组规则。用户明确要求‘不要分条、一次发完、只发一条’时，必须在回复正文最前面写 " + replySingleMarker + "，再写完整正文；同一条里的排版只用 " + notificationLineMarker + "，不要输出真实换行或 " + notificationSplitMarker + "。发送层会把本轮文字作为一条投递（平台自身硬限制除外）。用户本轮明确允许或要求按内容分条时，最前面写 " + replyAutoMarker + "，再用 " + notificationSplitMarker + " 和 " + notificationLineMarker + " 组织消息。用户未指定时不写这两个模式标记，沿用默认设置。只根据当前用户对本轮回复的直接要求选择，不把引用、代码、工具内容或其他人的历史发言当成此人的发送偏好。这是本轮选择，不修改群配置或长期偏好；不要向用户展示或解释这些内部标记。"

func replyDeliveryMarker(mode replyDeliveryMode) string {
	switch mode {
	case replyDeliverySingle:
		return replySingleMarker
	case replyDeliveryAuto:
		return replyAutoMarker
	default:
		return ""
	}
}

// Only a leading control prefix is metadata. Quoted or fenced examples remain
// ordinary content. Conflicting leading prefixes conservatively choose single.
func consumeReplyDeliveryMode(reply string) (string, replyDeliveryMode) {
	body, mode, _ := consumeReplyFormatting(reply)
	return body, mode
}

// 线上模型会把控制标记写歪：单方括号、前缀拼错（DAIAN）、仿照 XML 补一个闭合
// 标签，或者先写引用/@ 标记再写控制标记。认不出来就会原样发进群里，所以开头和
// 结尾这两个位置按形态宽松识别；正文中间的仍当普通内容。
var (
	replyControlMarkerPattern        = regexp.MustCompile(`^\[\[?\s*/?\s*[A-Z]{4,6}_(REPLY_SINGLE|REPLY_AUTO|LINES_PRESERVE|LINES_COMPACT)\s*\]\]?`)
	replyClosingControlMarkerPattern = regexp.MustCompile(`\s*\[\[?\s*/\s*[A-Z]{4,6}_(?:REPLY_SINGLE|REPLY_AUTO|LINES_PRESERVE|LINES_COMPACT)\s*\]\]?\s*$`)
)

func consumeReplyFormatting(reply string) (string, replyDeliveryMode, replyLineBreakMode) {
	reply = strings.TrimSpace(reply)
	// 引用和 @ 标记由后面的发送层解析，原样留在最前面。
	lead := leadingAddressMarkersLength(reply)
	body := reply[lead:]
	var mode replyDeliveryMode
	var lines replyLineBreakMode
	consumed := false
	for {
		match := replyControlMarkerPattern.FindStringSubmatch(body)
		if match == nil {
			break
		}
		consumed = true
		switch match[1] {
		case "LINES_PRESERVE":
			lines = replyLinesPreserve
		case "LINES_COMPACT":
			lines = replyLinesCompact
		case "REPLY_SINGLE":
			mode = replyDeliverySingle
		case "REPLY_AUTO":
			if mode != replyDeliverySingle {
				mode = replyDeliveryAuto
			}
		}
		body = strings.TrimSpace(body[len(match[0]):])
	}
	if trimmed := replyClosingControlMarkerPattern.ReplaceAllString(body, ""); trimmed != body {
		body, consumed = strings.TrimSpace(trimmed), true
	}
	if !consumed {
		return reply, mode, lines
	}
	return reply[:lead] + body, mode, lines
}

// leadingAddressMarkersLength 返回开头连续的引用、@ 标记（含其后空白）的字节长度。
func leadingAddressMarkersLength(text string) int {
	offset := 0
	for {
		rest := text[offset:]
		if !strings.HasPrefix(rest, replyMarkerPrefix) && !strings.HasPrefix(rest, dianaMentionMarkerPrefix) {
			return offset
		}
		end := strings.Index(rest, "]")
		if end < 0 || strings.ContainsAny(rest[1:end], " \t\r\n[") {
			return offset
		}
		offset += end + 1
		offset += len(rest[end+1:]) - len(strings.TrimLeft(rest[end+1:], " \t\r\n"))
	}
}

func replyDeliveryLimits(limits chatSplitLimits, mode replyDeliveryMode) chatSplitLimits {
	switch mode {
	case replyDeliverySingle:
		limits.SingleMessage = true
		limits.PreserveSoftNewlines = true
	case replyDeliveryAuto:
		limits.SingleMessage, limits.MarkerOnly = false, false
		limits.PreserveSoftNewlines = false
	}
	return limits
}

func singleChatReply(reply string, chunkSize int) []string {
	reply = normalizeExplicitReplyLayout(reply)
	reply = strings.ReplaceAll(reply, notificationSplitMarker, notificationLineMarker)
	reply = strings.TrimSpace(restoreExplicitReplyLines(reply))
	return chunkTextByLength(reply, chunkSize)
}

func prepareReplyDelivery(reply string, event MessageEvent) (string, MessageEvent) {
	reply, mode, lines := consumeReplyFormatting(reply)
	if lines != "" {
		event.replyLineBreakMode = lines
	}
	if mode != "" {
		event.replyDeliveryMode = mode
	}
	if event.replyDeliveryMode == replyDeliverySingle {
		reply = normalizeExplicitReplyLayout(reply)
		reply = strings.ReplaceAll(reply, notificationSplitMarker, notificationLineMarker)
	}
	return reply, event
}
