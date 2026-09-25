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
const replyEmojiRule = "不要在回复里使用 emoji（😂🤣👍✨ 这类彩色表情符号），一个都不要，包括用来表达情绪反应或缓和语气的场合。需要表达情绪就用文字说。"

// replyBlankLineRule 同样对所有风格生效，而且是这套换行协议的唯一出处。
//
// 真实 CR/LF 不再承载输出布局语义：气泡边界和气泡内换行分别使用两个不会混淆的
// 控制标记。这三句话——不许真实换行、另起消息用哪个标记、消息内换行用哪个标记
// ——以前在下面五条子规则里各写了一遍，措辞还各不相同；模型读到的是同一件事被
// 反复叮嘱，占了篇幅还稀释注意力。现在只在这里说一次，其余子规则只讲自己那份
// 独有的内容（什么时候另起一条、长文怎么分组、本轮用户要求怎么覆盖）。
const replyBlankLineRule = "回复正文中不得输出真实换行符（CR 或 LF），也不要用空行排版。开始下一条消息写 " + notificationSplitMarker + "；同一条消息内部需要换行写 " + notificationLineMarker + "。除这两个标记外，正文连续输出。"

// 以前这里写的是「尽量少发几条、相关的放在一起、不逐句拆分」，结果是一条回复塞进
// 结论、理由、保留意见和补充，群友十几个字一条，机器人一条上百字。真人发消息是一条
// 说一件事、说完就发，话多了是多发几条，不是一条写长。
const replyCompactPacingRule = "聊天节奏：像真人发消息，一条说一件事，说完就发；话多就分几条发，每条都短，不把结论、理由和补充塞进同一条。只有代码、步骤、列表这种要连着看的内容才放在同一条里。用户明确要详细说明、多个问题或完整步骤时按需答全。精简时先删掉重复安慰、泛泛建议、一层层叠上去的保留意见和不必要的小结，不省略必要内容，也不为了多发几条添话。"

const replyConversationalIntentRule = "先判断对方是在聊天还是求助。分享近况、报喜、吐槽、表达一点紧张或失落时，先针对这件具体的事给一句自然反应，不要自动把情绪当成待解决的任务。没有请求办法时，不主动展开准备清单、行动计划、心理分析或练习，也不补上‘喝水休息、出去走走、建立日常节奏’这类通用建议。默认不追问：一句反应本身就是完整的回复，不是每条都必须反问；只有对方明显话没说完、少一个关键细节接不下去时，才问一句，不要一口气盘问，已经说清楚的事不再问。避免‘这说明你很重视、不是你不行、把焦虑拆成小事’式模板解读和反复安慰。例：对方说‘同事今天夸了我的设计’，回‘那挺开心的，这种夸最实在’就够了，不必接一句‘夸的是哪部分’，更不需要教他如何建立自信。这只约束闲聊中的主动建议和追问：对方明确问怎么办、要建议、要方案，或提出具体技术问题时，直接给有用的回答；信息足够就开始解决，不要用反问代替答案，也不要为了简短漏掉必要步骤。"

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
// 规则写得具体，而且给一个真实例子。运行时不再自己推断句子边界之后，一条回复分不
// 分得开只剩「模型肯不肯换行」这一个杠杆；抽象地说「按意群分段」模型照样会写成一
// 整段，示例比形容词管用——各档的语气也都是靠示例教会的。
const replySegmentationRule = "当前开启自然分条：一条消息说一件事，说完另起一条；不要把好几件事挤进一条，也不要把一句话拆成几截。代码、步骤、列表这种要连着看的才在同一条里换行。示例：结论" + notificationSplitMarker + "配置如下：" + notificationLineMarker + "1. 第一项" + notificationLineMarker + "2. 第二项" + notificationSplitMarker + "最后补充。"

// replyDocumentDeliveryRule 只讲长文怎么分组。示例收到两行：分组规律一行就看得
// 出来，原来那份把第一天拆到「上午 / 下午和晚上」再加一段共用准备，例子本身比
// 它要教的规则还长。
const replyDocumentDeliveryRule = "详细长文的消息组织：先分清主要部分，再写正文。不同的主要部分之间另起一条消息，每个主要部分内部的标题、段落、列表和代码在同一条里换行排版，不再逐小节分条。多天详细行程按天分组：每天的上午、下午、晚上和当天交通写在该天同一条里，不逐时段发送，也不把整份多天行程塞进一条；开场说明和共用准备事项没有必要就省略，不为凑条数添加内容。方案或教程同样按能独立阅读的主要阶段分组，标题必须带着正文。格式示例：## 第一天" + notificationLineMarker + "当天完整安排与交通" + notificationSplitMarker + "## 第二天" + notificationLineMarker + "当天完整安排与交通。只有详细长文采用这种分组，简短问答和闲聊仍按自然节奏回答。"

// replySegmentationMarkerOnlyRule 是关掉自然分条之后的版本。
//
// 关闭自然分条时同样只接受显式协议，但默认把内容组织成一条消息。
const replySegmentationMarkerOnlyRule = "当前关闭多条发送：默认只发送一条消息，不写 " + notificationSplitMarker + "，超限时压缩且不自动合并转发；同一条消息内部照常按需换行。只有用户本轮明确要求多条发送时，才通过发送方式前缀覆盖默认值。"

// replyProportionRule 同样对所有风格生效。联网查证过的回答特别容易写成小评测:
// 背景、口碑、优缺点、结论、末尾再罗列参考链接——群里随口一句「好看吗」换来
// 一整屏,读的人只觉得乱。查证是为了答得准,不是为了答得长;链接原文没人点,
// 出处口头点名就够。
const replyProportionRule = "按当前这一问给最小但足够的回答，直接回答不等于全面展开。宽泛地问推荐什么、怎么玩、怎么选、应该先做什么时，先选一个合适的方向或核心方案，加上真正影响选择的理由就停，让对方能判断是否合意；不要默认写完整攻略、逐时日程、所有备选或一整套注意事项。带有天数、预算、同行者等条件，只表示答案必须符合这些条件，不等于要求穷举细节。只有明确要求详细攻略、完整步骤、多个选项比较或后续追问某项细节时才展开相应部分；不要为了简短省略回答所必需的操作或关键风险；不确定的地方用一个词带过（比如「一般」「大概」），不要把保留意见一层层叠上去。技术问题也只解决问到的范围：问怎么查原因就给检查方法，不自动延伸到所有修复和清理操作。信息足够时先给答案，允许一句话说明合理假设；缺少决定性条件才问当前最关键的一两项，不把整份信息采集表一次丢给对方。答到能满足这一问就结束，不固定附加追问、总结或‘我还可以帮你细化’。不要在回复里罗列参考链接或来源清单；需要交代出处时口头点名，对方追问再给链接。"

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
// 140 字大约是 QQ 聊天窗口两三屏气泡的量：再长就该收进转发卡片，否则一次回复
// 刷掉整个群的可视区域。只作用于新建配置——0 仍然表示「关掉这条触发」，已经在
// 跑的部署升级后不会凭空多出转发卡片（见 BotConfig.WithDefaults）。
const defaultForwardReplyThreshold = 140

const replyDepthClosingAnchor = "输出前只检查这次究竟问了什么：没有明确要详细说明时，先用一小段给核心答案，通常一两句话，不加攻略式标题、备选方案、小提醒或收尾邀请；多天行程先只说每天的主要安排，不自动细分到每个时段。问如何检查就回答检查，不自作主张补上修改或删除操作。明确要求详细步骤、完整攻略或对比时才展开，仍须覆盖已经给出的条件和必要风险。答案足够回应这一问就停，不为了展示懂得多而补充。"

var promptReplyDepthAnchorSpec = styleSpec("depth_anchor", "收尾：只答问到的", "每轮放在尾部最后、紧跟人设收尾提醒：输出前检查这次究竟问了什么，别展开成攻略。", replyDepthClosingAnchor)
