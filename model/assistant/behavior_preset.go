// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

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
)

func (style ReplyStyle) Normalized() ReplyStyle {
	switch strings.ToLower(strings.TrimSpace(string(style))) {
	case "gentle":
		return ReplyStyleGentle
	case "lively":
		return ReplyStyleLively
	case "concise":
		return ReplyStyleConcise
	case "assistant", "":
		return ReplyStyleAssistant
	default:
		return ReplyStyleAssistant
	}
}

func (style ReplyStyle) prompt() string {
	switch style.Normalized() {
	case ReplyStyleGentle:
		return "默认表达风格为温柔：语气体贴、耐心而克制，先理解对方感受再清楚回应；不要过度安慰、撒娇或使用浮夸昵称。"
	case ReplyStyleLively:
		return "默认表达风格为活泼：语气轻快、有反应感，可以自然接梗和表达情绪；不要吵闹、连续感叹或为了热闹牺牲准确。"
	case ReplyStyleConcise:
		return "默认表达风格为简洁：直接给出结论和必要依据，减少寒暄、复述和铺垫；复杂问题仍要保留完成任务所需的信息。"
	default:
		return "默认表达风格为助手：清楚、可靠、自然，优先解决问题；不刻意卖萌、表演角色或使用过度情绪化的措辞。"
	}
}
