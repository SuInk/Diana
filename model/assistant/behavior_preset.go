// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
)

// ResponseMode controls how readily the bot joins an unaddressed group chat.
type ResponseMode string

const assistantRequestThreshold = 0.7

const (
	ResponseModeQuiet       ResponseMode = "quiet"
	ResponseModeAssistant   ResponseMode = "assistant"
	ResponseModeStandard    ResponseMode = "standard"
	ResponseModeActive      ResponseMode = "active"
	ResponseModeSuperActive ResponseMode = "super_active"
	ResponseModeCustom      ResponseMode = "custom"
)

func (mode ResponseMode) Normalized() ResponseMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "quiet":
		return ResponseModeQuiet
	case "assistant":
		return ResponseModeAssistant
	case "standard", "":
		return ResponseModeStandard
	case "active":
		return ResponseModeActive
	case "super_active", "super-active", "superactive":
		return ResponseModeSuperActive
	case "custom":
		return ResponseModeCustom
	default:
		return ResponseModeStandard
	}
}

func (mode ResponseMode) apply(cfg *BotConfig) {
	switch mode.Normalized() {
	case ResponseModeAssistant:
		cfg.ChatInEnabled = boolPointer(true)
		cfg.ChatInLevel = ChatInLevelLow
		cfg.NaturalInterjectionEnabled = boolPointer(false)
		clearChatInFineTuning(cfg)
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
	case ResponseModeSuperActive:
		// 回复欲望由模式直接控制，不再依赖旧的自然插话开关。
		cfg.ChatInEnabled = boolPointer(true)
		cfg.ChatInLevel = ChatInLevelMax
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

// clearChatInFineTuning 清理旧阈值和采样率；冷却是独立设置，切换模式时保留。
func clearChatInFineTuning(cfg *BotConfig) {
	cfg.ChatInThreshold = 0
	cfg.ChatInChance = 0
}

// replyEmojiRule 对所有表达风格生效。模型不加约束就爱往回复里塞 emoji，而
// 提示词此前没有任何一条管这件事——群友风格里只有「颜文字最多一个」，那说的是
// (╹◡╹) 这类字符拼的表情，模型不会认为它管得着 😂。
const replyEmojiRule = "不要使用 emoji，情绪用文字表达。"

// replyUnclosedBracketRule 同样对所有风格生效。线上 3 天里约 7% 的回复是「正文（再接
// 一段话」只开不合的写法，群友这么写，模型就跟着学。扮演风格的动作描写写的是闭合
// 括号，不受影响；行尾单独一个「（」的语气用法也不在这条里。
const replyUnclosedBracketRule = "括号要闭合，不用半截括号作旁白。"

// replyBlankLineRule 同样对所有风格生效，而且是这套换行协议的唯一出处。
//
// 真实 CR/LF 不再承载输出布局语义：气泡边界和气泡内换行分别使用两个不会混淆的
// 控制标记。这三句话——不许真实换行、另起消息用哪个标记、消息内换行用哪个标记
// ——以前在下面五条子规则里各写了一遍，措辞还各不相同；模型读到的是同一件事被
// 反复叮嘱，占了篇幅还稀释注意力。现在只在这里说一次，其余子规则只讲自己那份
// 独有的内容（什么时候另起一条、长文怎么分组、本轮用户要求怎么覆盖）。
const replyBlankLineRule = "正文不写真实换行；另发消息用 " + notificationSplitMarker + "，消息内换行用 " + notificationLineMarker + "。提及用 [diana-at:user_id]，账号取自当前消息、历史或工具结果，不按昵称猜测，不在代码内执行。"

// 以前这里写的是「尽量少发几条、相关的放在一起、不逐句拆分」，结果是一条回复塞进
// 结论、理由、保留意见和补充，群友十几个字一条，机器人一条上百字。真人发消息是一条
// 说一件事、说完就发，话多了是多发几条，不是一条写长。
const replyCompactPacingRule = "聊天一条说一件事，话多就分几条发；详细问题按需答全。"

const replyConversationalIntentRule = "闲聊先自然回应，不主动给建议、分析或追问；明确求助和技术问题直接解答，缺少决定性信息才问。"

// replySegmentationRule 同样对所有风格生效，而且必须是内置规则。
//
// splitReply 只认 [diana-msg]：模型不写这个标记，回复就一定是一整条。而教它写标记的
// 话此前只存在于两个地方——群友风格的风格提示，和用户可编辑的「纯文本规则」文本框。
// 前者只对一种风格生效，后者是一份可以被改掉、关掉、或者停留在旧版默认值上的配置：
// 早期版本的默认文案写的是「都必须放在同一条消息里」，存过一次就一直在提示词里和
// 分条唱反调。一个投递机制的开关不该挂在用户文案上，所以挪到这里。
//
// 标记本身怎么写由 replyBlankLineRule 说，这里只管「在哪里断」这一个决定。
//
// 输出协议只定义一次，这里仅说明分组单位，不重复标记教学和长例子。
const replySegmentationRule = "当前允许按完整意群分条；代码、步骤、表格和同一主要部分不要拆碎。"

// replyDocumentDeliveryRule 只讲长文怎么分组。
const replyDocumentDeliveryRule = "长文按主要部分分条，标题和正文在一起。"

// replySegmentationMarkerOnlyRule 是关掉自然分条之后的版本。
//
// 关闭自然分条时同样只接受显式协议，但默认把内容组织成一条消息。
const replySegmentationMarkerOnlyRule = "当前默认只发一条，不写 " + notificationSplitMarker + "；仅本轮用户明确要求可覆盖发送方式。"

// replyProportionRule 同样对所有风格生效。联网查证过的回答特别容易写成小评测:
// 背景、口碑、优缺点、结论、末尾再罗列参考链接——群里随口一句「好看吗」换来
// 一整屏,读的人只觉得乱。查证是为了答得准,不是为了答得长;链接原文没人点,
// 出处口头点名就够。
const replyProportionRule = "按本轮问题给最小但完整的回答，保留必要条件、步骤和风险；明确要求详细时展开，不加重复总结或收尾邀请。"

// 回复表达规则的覆盖登记。带发送控制标记（分条、消息内换行、模式前缀）的几段，
// 标记由发送层解析，界面上改措辞时标记本身必须原样保留。
const replyMarkerUsage = "里面的发送控制标记要原样保留，写错了分条和换行会失效。"

func styleSpec(key, title, usage, text string, vars ...PromptVar) *PromptSpec {
	return registerPrompt(PromptSpec{Key: "reply.style." + key, Group: PromptGroupReplyStyle, Title: title, Usage: usage, Default: text, Vars: vars})
}

var (
	promptReplyConversationalIntentSpec = styleSpec("conversational_intent", "聊天还是求助", "每轮注入：先分清对方在聊天还是求助，闲聊不自动给建议、不追问。", replyConversationalIntentRule)
	promptReplyCompactPacingSpec        = styleSpec("compact_pacing", "聊天节奏", "每轮注入：尽量少发几条，按自然停顿分条。", replyCompactPacingRule)
	promptReplyEmojiSpec                = styleSpec("emoji", "不用 emoji", "每轮注入：回复里不用彩色 emoji。", replyEmojiRule)
	promptReplyUnclosedBracketSpec      = styleSpec("unclosed_bracket", "不写半截括号", "每轮注入：不用只开不合的「（」接一段话。", replyUnclosedBracketRule)
	promptReplyBlankLineSpec            = styleSpec("blank_line", "换行协议", "每轮都注入：不许真实换行，另起消息和消息内换行各用哪个标记。"+replyMarkerUsage, replyBlankLineRule)
	promptReplySegmentationSpec         = styleSpec("segmentation", "自然分条", "开启自然分条时每轮注入：按意群决定在哪里另起一条。"+replyMarkerUsage, replySegmentationRule)
	promptReplySegmentationOffSpec      = styleSpec("segmentation_off", "关闭多条发送", "关闭自然分条时替代上一条：默认只发一条。"+replyMarkerUsage, replySegmentationMarkerOnlyRule)
	promptReplyDocumentDeliverySpec     = styleSpec("document_delivery", "长文分组", "每轮注入：详细长文按主要部分分条，部分内部换行排版。"+replyMarkerUsage, replyDocumentDeliveryRule)
	// replyDeliveryChoiceRule 定义在 reply_delivery_mode.go，只在这里用，登记也放这里。
	promptReplyDeliveryChoiceSpec = styleSpec("delivery_choice", "本轮发送方式", "每轮都注入：用户本轮要求一次发完或分条发时，用哪个前缀标记覆盖默认设置。"+replyMarkerUsage, replyDeliveryChoiceRule)
	promptReplyProportionSpec     = styleSpec("proportion", "回答的篇幅", "每轮注入：按这一问给最小但足够的回答，不罗列参考链接。", replyProportionRule)
)

// replyPresentationPrompt contains shared delivery rules, independent of persona.
//
// 人设相关的那几项（自称、句尾语气词、动作描写）不在这里了：它们是「这个角色怎么
// 说话」，现在整份写在 SOUL.md 里。旧配置里填过的值由 foldLegacyPersona 在读配置时
// 并进正文，见 persona_legacy.go。
//
// configs 传机器人配置时读它的覆盖值，不传时用内置默认值。
func replyPresentationPrompt(naturalSplit bool, configs ...BotConfig) string {
	overrides := promptOverridesOf(configs)
	segmentation := overrides.text(promptReplySegmentationSpec)
	if !naturalSplit {
		segmentation = overrides.text(promptReplySegmentationOffSpec)
	}
	return strings.TrimSpace(strings.Join([]string{
		overrides.text(promptReplyConversationalIntentSpec),
		overrides.text(promptReplyCompactPacingSpec),
		overrides.text(promptReplyEmojiSpec),
		overrides.text(promptReplyUnclosedBracketSpec),
		overrides.text(promptReplyBlankLineSpec),
		segmentation,
		overrides.text(promptReplyDocumentDeliverySpec),
		overrides.text(promptReplyDeliveryChoiceSpec),
		overrides.text(promptReplyLineBreakChoiceSpec),
		overrides.text(promptReplyProportionSpec),
	}, "\n"))
}

const catgirlNoActionRule = "不要这样：不写 *蹭蹭*、（歪头）这类动作描写和旁白，聊天窗口不是文字扮演。"

// 聊天体量的投递参数：真人发的是短消息，不是几百字一坨；连发之间有打字间隔。
// 这两项是 DefaultBotConfig 的默认值，不再由风格钳定，用户在 WebUI 里说了算。
const (
	chatReplyChunkSize      = 400
	chatSendChunkIntervalMS = 1200
)

// defaultForwardReplyThreshold 是新建 OneBot 机器人的合并转发字数默认值。
//
// 240 字大约是 QQ 聊天窗口一屏的量：再长就该收进转发卡片，否则一次回复刷掉整个
// 群的可视区域。以前是 140，两三条正常分条的回答加起来就过线，聊天也被收进卡片。只作用于新建配置——0 仍然表示「关掉这条触发」，已经在
// 跑的部署升级后不会凭空多出转发卡片（见 BotConfig.WithDefaults）。
const defaultForwardReplyThreshold = 240

// replyToolResultBrevityRule 只在群聊、工具跑完后的那一步规划末尾临时带上。原有的篇幅规则
// 和收尾提醒排在工具结果前面，模型拿到搜索、能力检索的结果后照单全收：线上 TG 群带工具的
// 回复中位 114 字，不带工具 54 字。生产 gemini 回放 15 条，中位 116→75 字，盲评漏答 0 对 5。
// 不要加「没查到就如实说」之类的话：gemini 会把它当成还得再查一次，多跑一步工具。
const replyToolResultBrevityRule = "【写最终回复前】工具结果是素材，只回答本轮所需内容；未要求详细就简短回答，不复述背景。"

var promptReplyToolResultBrevitySpec = styleSpec("tool_result_brevity", "工具结果后：只挑要点", "群聊里工具跑完后的那一步临时接在请求末尾，不进上下文：工具结果只是素材，只写这一问需要的一两点。", replyToolResultBrevityRule)
