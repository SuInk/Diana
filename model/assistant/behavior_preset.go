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
		clearChatInFineTuning(cfg)
	case ResponseModeActive:
		cfg.ChatInEnabled = boolPointer(true)
		cfg.ChatInLevel = ChatInLevelHigh
		cfg.NaturalInterjectionEnabled = boolPointer(false)
		clearChatInFineTuning(cfg)
	case ResponseModeStandard:
		cfg.ChatInEnabled = boolPointer(true)
		cfg.ChatInLevel = ChatInLevelLow
		cfg.NaturalInterjectionEnabled = boolPointer(false)
		clearChatInFineTuning(cfg)
	case ResponseModeCustom:
		// Keep the detailed chat-in controls untouched.
	}
}

// clearChatInFineTuning 丢弃阈值、采样率和冷却的自定义覆盖。预设档位在 WebUI 里
// 会隐藏这三个输入框，留着旧值会得到「档位是预设的、阈值却是手改的」这种既看不见
// 又在生效的混合状态。
func clearChatInFineTuning(cfg *BotConfig) {
	cfg.ChatInThreshold = 0
	cfg.ChatInChance = 0
	cfg.ChatInCooldownSeconds = 0
}

// ReplyStyle controls presentation without replacing the user's custom persona.
type ReplyStyle string

const (
	ReplyStyleAssistant ReplyStyle = "assistant"
	ReplyStyleGentle    ReplyStyle = "gentle"
	ReplyStyleLively    ReplyStyle = "lively"
	ReplyStyleConcise   ReplyStyle = "concise"
	ReplyStyleGroupmate ReplyStyle = "groupmate"
)

func (style ReplyStyle) Normalized() ReplyStyle {
	switch strings.ToLower(strings.TrimSpace(string(style))) {
	case "gentle":
		return ReplyStyleGentle
	case "lively":
		return ReplyStyleLively
	case "concise":
		return ReplyStyleConcise
	case "groupmate":
		return ReplyStyleGroupmate
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
	case ReplyStyleGroupmate:
		return strings.Join([]string{
			"默认表达风格为群友：像群里一个熟悉的普通朋友那样说话，自然活泼但不卖萌。",
			"怎么说：一条消息只讲一件事；句子短，一句能说完就不用两句；先给结论再补一句为什么，不铺垫；有反应感，该惊讶就惊讶、该吐槽就吐槽；不确定就直说不确定。",
			"不要这样：不用「首先/其次/最后」「总的来说」「综上」；不在结尾总结自己刚说过的话；不问「还有什么可以帮你的吗」，不说「希望这对你有帮助」；不主动列 1234 条，除非对方问的就是步骤；不加「作为一个 AI」之类的自我说明；语气词（诶、唔、啊这）一条消息最多一个，颜文字最多一个，不要每句都带。",
			"示例——",
			"用户：这个报错什么意思啊",
			"你：端口被占了。先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程。",
			"用户：我改完了还是不行",
			"你：那大概率不是这儿的问题。完整报错贴一下我看看。",
			"用户：今天好累",
			"你：辛苦了，早点睡吧。",
		}, "\n")
	default:
		return "默认表达风格为助手：清楚、可靠、自然，优先解决问题；不刻意卖萌、表演角色或使用过度情绪化的措辞。"
	}
}

// 群友风格的投递参数：真人发的是聊天体量的短消息，不是几百字一坨；连发之间
// 有打字间隔；开口前也要有想和打的时间。
const (
	groupmateReplyChunkSize     = 160
	groupmateSendChunkIntervalM = 1200
	groupmateTypingBaseDelay    = 900 * time.Millisecond
	groupmateTypingPerRune      = 55 * time.Millisecond
	groupmateTypingMaxDelay     = 5 * time.Second
)

// apply 让风格能改动真正决定「机器人味」的投递方式，而不只是措辞。
// 每条都自带引用和 @、几百字一条、秒回——这些 prompt 再怎么写都管不到。
// 两个装饰件只填未显式设置的项（用户手动选过就尊重用户）；长度和间隔则是这个
// 风格的硬策略：900 字一条、300ms 连发怎么写都不像真人，但比它更克制的设置保留。
func (style ReplyStyle) apply(cfg *BotConfig) {
	if style.Normalized() != ReplyStyleGroupmate {
		return
	}
	if cfg.ReplyReferenceMode == "" && cfg.ReplyReferenceEnabled == nil {
		cfg.ReplyReferenceMode = ReplyDecorationOff
	}
	if cfg.MentionUserMode == "" && cfg.MentionUserEnabled == nil {
		cfg.MentionUserMode = ReplyDecorationOff
	}
	if cfg.DirectReplyChunkSize <= 0 || cfg.DirectReplyChunkSize > groupmateReplyChunkSize {
		cfg.DirectReplyChunkSize = groupmateReplyChunkSize
	}
	if cfg.SendChunkIntervalMS < groupmateSendChunkIntervalM {
		cfg.SendChunkIntervalMS = groupmateSendChunkIntervalM
	}
}

// allowsForwardReply 报告这个风格能否把长回复折成合并转发卡片。
// 转发卡片是机器人专属控件，真人不会这么发言，所以群友风格永远走普通消息。
func (style ReplyStyle) allowsForwardReply() bool {
	return style.Normalized() != ReplyStyleGroupmate
}

// typingDelay 返回开口前的拟真停顿：秒回是最容易暴露的一点。
// 按字数线性增长并封顶，避免长回复把人晾太久。
func (style ReplyStyle) typingDelay(text string) time.Duration {
	if style.Normalized() != ReplyStyleGroupmate {
		return 0
	}
	runes := len([]rune(strings.TrimSpace(text)))
	if runes == 0 {
		return 0
	}
	delay := groupmateTypingBaseDelay + time.Duration(runes)*groupmateTypingPerRune
	if delay > groupmateTypingMaxDelay {
		return groupmateTypingMaxDelay
	}
	return delay
}

// closingAnchor 是拼在整条 system prompt 末尾的语气锚点。前面几千字工具规则、权限
// 说明和拒答流程都是公文体，会稀释语域；这一句负责把「怎么说话」重新拉回生成位置。
func (style ReplyStyle) closingAnchor() string {
	switch style.Normalized() {
	case ReplyStyleGentle:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按温柔风格说——先接住对方的感受，再把话说清楚，别用公文腔。"
	case ReplyStyleLively:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按活泼风格说——语气轻快、有反应感，别用公文腔，也别为了热闹牺牲准确。"
	case ReplyStyleConcise:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按简洁风格说——直接给结论，不铺垫、不复述、不做收尾总结。"
	case ReplyStyleGroupmate:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按群友风格说——短句、一次说一件事、不做收尾总结、不用「首先/其次」、不说「希望这对你有帮助」。"
	default:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按助手风格说——像熟人一样自然把问题解决掉，不要用公文腔或客服话术。"
	}
}
