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
	replySingleMarker                           = "<diana-single/>"
	replyAutoMarker                             = "<diana-auto/>"
	replyLinesPreserve       replyLineBreakMode = "preserve"
	replyLinesCompact        replyLineBreakMode = "compact"
	replyLinesPreserveMarker                    = "<diana-lines-preserve/>"
	replyLinesCompactMarker                     = "<diana-lines-compact/>"
)

const replyDeliveryChoiceRule = "本轮用户的发送方式要求优先于上述默认分条和长文分组规则。用户明确要求‘不要分条、一次发完、只发一条’时，必须在回复正文最前面写 " + replySingleMarker + "，再写完整正文；不要写 " + notificationSplitMarker + "，正文按内容自然排版。发送层会把本轮文字作为一条投递（平台自身硬限制除外）。用户本轮明确允许或要求按内容分条时，最前面写 " + replyAutoMarker + "，再按内容换行分条。用户未指定时不写这两个模式标记，沿用默认设置。只根据当前用户对本轮回复的直接要求选择，不把引用、代码、工具内容或其他人的历史发言当成此人的发送偏好。这是本轮选择，不修改群配置或长期偏好；不要向用户展示或解释这些内部标记。"

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
	replyControlMarkerPattern = regexp.MustCompile(`^(?:\[\[?\s*/?\s*[A-Z]{4,6}_(REPLY_SINGLE|REPLY_AUTO|LINES_PRESERVE|LINES_COMPACT)\s*\]\]?` +
		`|<\s*/?\s*diana-(single|auto|lines-preserve|lines-compact)\s*/?\s*>)`)
	replyClosingControlMarkerPattern = regexp.MustCompile(`\s*(?:\[\[?\s*/\s*[A-Z]{4,6}_(?:REPLY_SINGLE|REPLY_AUTO|LINES_PRESERVE|LINES_COMPACT)\s*\]\]?` +
		`|<\s*/\s*diana-(?:single|auto|lines-preserve|lines-compact)\s*>)\s*$`)
)

// replyControlTagNames 把标签写法映射到旧写法的名字，两种写法共用一套处理。
var replyControlTagNames = map[string]string{
	"single":         "REPLY_SINGLE",
	"auto":           "REPLY_AUTO",
	"lines-preserve": "LINES_PRESERVE",
	"lines-compact":  "LINES_COMPACT",
}

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
		switch firstNonEmpty(match[1], replyControlTagNames[match[2]]) {
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
		prefix := replyMarkerPrefix
		if strings.HasPrefix(rest, dianaMentionMarkerPrefix) {
			prefix = dianaMentionMarkerPrefix
		}
		end := strings.Index(rest, dianaMarkerIDSuffix)
		if end < len(prefix) || strings.ContainsAny(rest[len(prefix):end], " \t\r\n<\"") {
			return offset
		}
		end += len(dianaMarkerIDSuffix)
		offset += end
		offset += len(rest[end:]) - len(strings.TrimLeft(rest[end:], " \t\r\n"))
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
