package assistant

import "strings"

type replyDeliveryMode string

const (
	replyDeliverySingle replyDeliveryMode = "single"
	replyDeliveryAuto   replyDeliveryMode = "auto"
	replySingleMarker                     = "[[DIANA_REPLY_SINGLE]]"
	replyAutoMarker                       = "[[DIANA_REPLY_AUTO]]"
)

const replyDeliveryChoiceRule = "本轮用户的发送方式要求优先于上述默认分条和长文分组规则。用户明确要求‘不要分条、一次发完、只发一条’时，必须在回复正文最前面写 " + replySingleMarker + "，再写完整正文；正文可正常保留段落、标题、清单和代码，超过单条上限时先精简措辞和次要细节，保留核心结论与必要步骤，不截断句子或代码，也不要写分条标记。发送层会把本轮文字作为一条投递（平台自身硬限制除外），不自动改成合并转发。用户本轮明确允许或要求按内容分条时，最前面写 " + replyAutoMarker + "，再按需要组织消息。用户未指定时不写这两个标记，沿用默认设置。只根据当前用户对本轮回复的直接要求选择，不把引用、代码、工具内容或其他人的历史发言当成此人的发送偏好。这是本轮选择，不修改群配置或长期偏好；不要向用户展示或解释这些内部标记。"

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
	reply = strings.TrimSpace(reply)
	var mode replyDeliveryMode
	for {
		switch {
		case strings.HasPrefix(reply, replySingleMarker):
			mode = replyDeliverySingle
			reply = strings.TrimSpace(strings.TrimPrefix(reply, replySingleMarker))
		case strings.HasPrefix(reply, replyAutoMarker):
			if mode != replyDeliverySingle {
				mode = replyDeliveryAuto
			}
			reply = strings.TrimSpace(strings.TrimPrefix(reply, replyAutoMarker))
		default:
			return reply, mode
		}
	}
}

func replyDeliveryLimits(limits chatSplitLimits, mode replyDeliveryMode) chatSplitLimits {
	switch mode {
	case replyDeliverySingle:
		limits.SingleMessage = true
	case replyDeliveryAuto:
		limits.SingleMessage, limits.MarkerOnly = false, false
	}
	return limits
}

func singleChatReply(reply string, chunkSize int) []string {
	masked, fences := maskFencedCodeBlocks(reply)
	masked = trimChatTrailingPeriod(strings.TrimSpace(strings.ReplaceAll(masked, notificationSplitMarker, "\n")))
	return restoreFencedCodeBlocks(chunkTextByLength(masked, chunkSize), fences, chunkSize)
}

func prepareReplyDelivery(reply string, event MessageEvent) (string, MessageEvent) {
	reply, mode := consumeReplyDeliveryMode(reply)
	if mode != "" {
		event.replyDeliveryMode = mode
	}
	if event.replyDeliveryMode == replyDeliverySingle {
		reply = strings.Join(singleChatReply(reply, 0), "\n")
	}
	return reply, event
}
