package assistant

import "strings"

// Proactive participation should not inherit a persona's multi-bubble delivery.
// Length limits remain a transport fallback, never a reason to truncate content.
func splitEventChatReply(reply string, cfg BotConfig, event MessageEvent) []string {
	if event.Kind != EventKindGroup || (!event.proactiveReply && !event.chatInReply) {
		return splitChatReply(reply, chatSplitLimitsFrom(cfg))
	}
	reply = strings.ReplaceAll(normalizeSplitMarkers(reply), notificationSplitMarker, "\n")
	return splitChatReply(reply, chatSplitLimits{ChunkSize: notificationChunkSize, MaxBubbles: 1, MarkerOnly: true})
}

func supportsOneBotGroupTool(cfg BotConfig, event MessageEvent) bool {
	return event.Kind == EventKindGroup && NormalizePlatformID(cfg.Platform) == PlatformOneBotV11
}

const proactiveReplyPacingPrompt = `主动接话的发送节奏：默认只写一条简短消息，一两句说完，不使用分条标记，不把动作描写单独写成一段。同一发言者连续补充的内容合起来回答，不逐条复述再各答一遍。有人反馈你太吵或要求减少发言时，尊重这个反馈，不用多段道歉或动作表演继续占屏；需要回应时一句即可。读取配置不等于修改配置，工具失败不等于执行成功；没有成功的修改结果，不得声称已降低频率、已静音或已改好设置。`
