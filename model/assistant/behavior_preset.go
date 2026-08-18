// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

// ResponseMode controls how readily the bot joins an unaddressed group chat.
type ResponseMode string

const (
	ResponseModeQuiet    ResponseMode = "quiet"
	ResponseModeStandard ResponseMode = "standard"
	ResponseModeActive   ResponseMode = "active"
	ResponseModeCustom   ResponseMode = "custom"
)

func (mode ResponseMode) Normalized() ResponseMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "quiet":
		return ResponseModeQuiet
	case "standard", "":
		return ResponseModeStandard
	case "active":
		return ResponseModeActive
	case "custom":
		return ResponseModeCustom
	default:
		return ResponseModeStandard
	}
}

func (mode ResponseMode) apply(cfg *BotConfig) {
	switch mode.Normalized() {
	case ResponseModeQuiet:
		cfg.ChatInEnabled = boolPointer(false)
		cfg.ChatInLevel = ChatInLevelOff
		cfg.NaturalInterjectionEnabled = boolPointer(false)
	case ResponseModeActive:
		cfg.ChatInEnabled = boolPointer(true)
		cfg.ChatInLevel = ChatInLevelHigh
		cfg.NaturalInterjectionEnabled = boolPointer(false)
	case ResponseModeStandard:
		cfg.ChatInEnabled = boolPointer(true)
		cfg.ChatInLevel = ChatInLevelLow
		cfg.NaturalInterjectionEnabled = boolPointer(false)
	case ResponseModeCustom:
		// Keep the detailed chat-in controls untouched.
	}
}

// ReplyStyle controls presentation without replacing the user's custom persona.
type ReplyStyle string

const (
	ReplyStyleAssistant ReplyStyle = "assistant"
	ReplyStyleGentle    ReplyStyle = "gentle"
	ReplyStyleLively    ReplyStyle = "lively"
	ReplyStyleConcise   ReplyStyle = "concise"
	ReplyStyleMember    ReplyStyle = "member"
)

func (style ReplyStyle) Normalized() ReplyStyle {
	switch strings.ToLower(strings.TrimSpace(string(style))) {
	case "gentle":
		return ReplyStyleGentle
	case "lively":
		return ReplyStyleLively
	case "concise":
		return ReplyStyleConcise
	case "member":
		return ReplyStyleMember
	case "assistant", "":
		return ReplyStyleAssistant
	default:
		return ReplyStyleAssistant
	}
}

// 群友风格的投递参数：真人发的是聊天体量的短消息，不是几百字一坨；连发之间
// 有打字间隔；开口前也要有想和打的时间。
const (
	memberReplyChunkSize     = 160
	memberSendChunkIntervalM = 1200
	memberTypingBaseDelay    = 900 * time.Millisecond
	memberTypingPerRune      = 55 * time.Millisecond
	memberTypingMaxDelay     = 5 * time.Second
)

// apply 让风格能改动真正决定「机器人味」的投递方式，而不只是措辞。
// 每条都自带引用和 @、几百字一条、秒回——这些 prompt 再怎么写都管不到。
// 两个开关只填未显式设置的项（用户手动开过就尊重用户）；长度和间隔则是这个
// 风格的硬策略：900 字一条、300ms 连发怎么写都不像真人，但比它更克制的设置保留。
func (style ReplyStyle) apply(cfg *BotConfig) {
	if style.Normalized() != ReplyStyleMember {
		return
	}
	if cfg.ReplyReferenceEnabled == nil {
		cfg.ReplyReferenceEnabled = boolPointer(false)
	}
	if cfg.MentionUserEnabled == nil {
		cfg.MentionUserEnabled = boolPointer(false)
	}
	if cfg.DirectReplyChunkSize <= 0 || cfg.DirectReplyChunkSize > memberReplyChunkSize {
		cfg.DirectReplyChunkSize = memberReplyChunkSize
	}
	if cfg.SendChunkIntervalMS < memberSendChunkIntervalM {
		cfg.SendChunkIntervalMS = memberSendChunkIntervalM
	}
}

// allowsForwardReply 报告这个风格能否把长回复折成合并转发卡片。
// 转发卡片是机器人专属控件，真人不会这么发言，所以群友风格永远走普通消息。
func (style ReplyStyle) allowsForwardReply() bool {
	return style.Normalized() != ReplyStyleMember
}

// typingDelay 返回开口前的拟真停顿：秒回是最容易暴露的一点。
// 按字数线性增长并封顶，避免长回复把人晾太久。
func (style ReplyStyle) typingDelay(text string) time.Duration {
	if style.Normalized() != ReplyStyleMember {
		return 0
	}
	runes := len([]rune(strings.TrimSpace(text)))
	if runes == 0 {
		return 0
	}
	delay := memberTypingBaseDelay + time.Duration(runes)*memberTypingPerRune
	if delay > memberTypingMaxDelay {
		return memberTypingMaxDelay
	}
	return delay
}

func (style ReplyStyle) prompt() string {
	switch style.Normalized() {
	case ReplyStyleGentle:
		return "默认表达风格为温柔：语气体贴、耐心而克制，先理解对方感受再清楚回应；不要过度安慰、撒娇或使用浮夸昵称。"
	case ReplyStyleLively:
		return "默认表达风格为活泼：语气轻快、有反应感，可以自然接梗和表达情绪；不要吵闹、连续感叹或为了热闹牺牲准确。"
	case ReplyStyleConcise:
		return "默认表达风格为简洁：直接给出结论和必要依据，减少寒暄、复述和铺垫；复杂问题仍要保留完成任务所需的信息。"
	case ReplyStyleMember:
		// 真人在群里是短句、口语、一次说一件事，最露馅的是列表、复述和「还有什么需要帮忙的吗」这类客服腔。
		return "默认表达风格为群友：像群里的普通成员那样说话，短句、口语、一次只说一件事，不用列表编号和「首先／其次」这类书面结构，也不复述对方的话或用「还有什么需要帮忙的吗」收尾；不要硬玩梗、堆表情或强行找话说，没什么可说时简单接一句就行，被直接问起是不是机器人时如实回答。"
	default:
		return "默认表达风格为助手：清楚、可靠、自然，优先解决问题；不刻意卖萌、表演角色或使用过度情绪化的措辞。"
	}
}
