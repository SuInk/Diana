package assistant

// Only casual interjections suppress inferred splits. Explicit model markers
// still apply; routed requests use normal settings.
func splitEventChatReply(reply string, cfg BotConfig, event MessageEvent) []string {
	return splitChatReply(reply, chatSplitLimitsForEvent(cfg, event))
}

// Prompt construction and delivery must agree on whether a newline splits.
func chatSplitLimitsForEvent(cfg BotConfig, event MessageEvent) chatSplitLimits {
	limits := chatSplitLimitsFrom(cfg)
	if event.replyDeliveryMode != "" {
		return replyDeliveryLimits(limits, event.replyDeliveryMode)
	}
	if event.Kind != EventKindGroup || !event.chatInReply {
		return limits
	}
	limits.MarkerOnly = true
	return limits
}

func supportsOneBotGroupTool(cfg BotConfig, event MessageEvent) bool {
	return event.Kind == EventKindGroup && NormalizePlatformID(cfg.Platform) == PlatformOneBotV11
}

const proactiveReplyPacingPrompt = `闲聊插话的发送节奏：默认只写一条简短消息，一两句说完；确实需要分开发言时，可以使用 ` + notificationSplitMarker + ` 显式分条。没有显式分条标记时，换行只作排版，不额外拆成多个气泡。不把动作描写单独写成一段。同一发言者连续补充的内容合起来回答，不逐条复述再各答一遍。有人反馈你太吵或要求减少发言时，尊重这个反馈，不用多段道歉或动作表演继续占屏；需要回应时一句即可。`

const proactiveReplyToolResultPrompt = `读取配置不等于修改配置，工具失败不等于执行成功；没有成功的修改结果，不得声称已降低频率、已静音或已改好设置。`
