// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// ReplyDecorationMode 描述群聊回复的装饰件（引用原消息、@ 发送者）由谁决定。
// 布尔开关只能表达「每条都带」或「一条都不带」，两种极端都不像真人：真人只在
// 需要指向具体某条消息或需要点名时才引用和 @。auto 把这个判断交给模型自己。
type ReplyDecorationMode string

const (
	// ReplyDecorationOn 每条群聊回复都带上该装饰件。
	ReplyDecorationOn ReplyDecorationMode = "on"
	// ReplyDecorationOff 永不带该装饰件。
	ReplyDecorationOff ReplyDecorationMode = "off"
	// ReplyDecorationAuto 运行时不自动添加，由模型在正文里自行写出引用标记或 @。
	ReplyDecorationAuto ReplyDecorationMode = "auto"
)

// normalizeReplyDecorationMode 归一化装饰件模式。mode 为空时回落到旧的布尔开关，
// 让升级前保存的配置保持原有行为；两者都没有时按 on（历史默认值）处理。
func normalizeReplyDecorationMode(mode ReplyDecorationMode, legacy *bool) ReplyDecorationMode {
	switch ReplyDecorationMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ReplyDecorationOn:
		return ReplyDecorationOn
	case ReplyDecorationOff:
		return ReplyDecorationOff
	case ReplyDecorationAuto:
		return ReplyDecorationAuto
	}
	if legacy != nil && !*legacy {
		return ReplyDecorationOff
	}
	return ReplyDecorationOn
}

// replyReferenceMode 返回本次生效的引用模式，未归一化的配置也能安全读取。
func replyReferenceMode(cfg BotConfig) ReplyDecorationMode {
	return normalizeReplyDecorationMode(cfg.ReplyReferenceMode, cfg.ReplyReferenceEnabled)
}

// mentionUserMode 返回本次生效的 @ 模式，未归一化的配置也能安全读取。
func mentionUserMode(cfg BotConfig) ReplyDecorationMode {
	return normalizeReplyDecorationMode(cfg.MentionUserMode, cfg.MentionUserEnabled)
}

// replyDecorationPrompt 只在 auto 模式下告诉模型怎么自己带引用和 @。返回值包含
// 当前消息 ID，逐条消息都不同，因此和实时时钟一样只能作为尾部独立 system 消息注入，
// 不能拼进人设提示词——否则那段最长的前缀每条消息都会失效一次。
func replyDecorationPrompt(cfg BotConfig, event MessageEvent) string {
	if event.Kind != EventKindGroup {
		return ""
	}
	var builder strings.Builder
	if replyReferenceMode(cfg) == ReplyDecorationAuto {
		if messageID := strings.TrimSpace(event.MessageID); validOutgoingReplyMessageID(messageID) {
			builder.WriteString("本次是否引用原消息由你自己决定：话题跳转、隔了几轮才回应、或群里同时有多个话题时，在回复最开头写 " +
				replyMarkerPrefix + messageID + "] 来指向当前这条消息；正常一问一答、连续对话时不要引用。整段标记必须写在最开头，正文里不要出现。")
		}
	}
	if mentionUserMode(cfg) == ReplyDecorationAuto {
		if userID := strings.TrimSpace(event.UserID); userID != "" {
			appendPromptSection(&builder, "本次是否 @ 发送者由你自己决定：多人同时说话需要点名、或对方可能已经走开时才写 @"+userID+
				"；一对一顺畅接话、对方刚刚说完话时不要 @，每句都 @ 很像机器人。")
		}
	}
	return strings.TrimSpace(builder.String())
}
