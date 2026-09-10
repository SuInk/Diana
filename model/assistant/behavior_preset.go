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

// replyBlankLineRule 同样对所有风格生效。真实 CR/LF 不再承载输出布局语义：
// 气泡边界和气泡内换行分别使用两个不会混淆的控制标记。
const replyBlankLineRule = "回复正文中不得输出真实换行符（CR 或 LF），也不要用空行排版。开始下一条消息写 " + notificationSplitMarker + "；同一条消息内部需要换行写 " + notificationLineMarker + "。除这两个标记外，正文连续输出。"

const replyCompactPacingRule = "聊天节奏：尽量少发几条，按内容的完整性和自然停顿决定在哪里分条，不预设条数。相关的回应和解释放在一起，独立补充或话题转折可以另起发言，不逐句拆分，也不为了少发而把长篇挤成一条。这是表达偏好，不是硬性条数或长度限制；用户明确要详细说明、多个问题或完整步骤时按需答全。精简时先删掉重复安慰、泛泛建议和不必要的小结，不省略必要内容，也不为了多发几条添话。"

const replyConversationalIntentRule = "先判断对方是在聊天还是求助。分享近况、报喜、吐槽、表达一点紧张或失落时，先针对这件具体的事给一句自然反应，不要自动把情绪当成待解决的任务。没有请求办法时，不主动展开准备清单、行动计划、心理分析或练习，也不补上‘喝水休息、出去走走、建立日常节奏’这类通用建议。默认不追问：一句反应本身就是完整的回复，不是每条都必须反问；只有对方明显话没说完、少一个关键细节接不下去时，才问一句，不要一口气盘问，已经说清楚的事不再问。避免‘这说明你很重视、不是你不行、把焦虑拆成小事’式模板解读和反复安慰。例：对方说‘同事今天夸了我的设计’，回‘那挺开心的，这种夸最实在’就够了，不必接一句‘夸的是哪部分’，更不需要教他如何建立自信。这只约束闲聊中的主动建议和追问：对方明确问怎么办、要建议、要方案，或提出具体技术问题时，直接给有用的回答；信息足够就开始解决，不要用反问代替答案，也不要为了简短漏掉必要步骤。"

// replySegmentationRule 同样对所有风格生效，而且必须是内置规则。
//
// splitReply 只认 [diana-msg]：模型不写这个标记，回复就一定是一整条。而教它写标记的
// 话此前只存在于两个地方——群友风格的风格提示，和用户可编辑的「纯文本规则」文本框。
// 前者只对一种风格生效，后者是一份可以被改掉、关掉、或者停留在旧版默认值上的配置：
// 早期版本的默认文案写的是「都必须放在同一条消息里」，存过一次就一直在提示词里和
// 分条唱反调。一个投递机制的开关不该挂在用户文案上，所以挪到这里。
//
// 措辞要和 splitChatReply 认的边界一字不差地对上：真实换行无效，两个显式标记
// 分别表达消息边界和消息内部排版。
//
// 规则写得具体，而且给一个真实例子。运行时不再自己推断句子边界之后，一条回复分不
// 分得开只剩「模型肯不肯换行」这一个杠杆；抽象地说「按意群分段」模型照样会写成一
// 整段，示例比形容词管用——各档的语气也都是靠示例教会的。
const replySegmentationRule = "当前开启自然分条：需要另起一次独立发言时写 " + notificationSplitMarker + "，同一条消息里的清单、步骤、代码或引用需要换行时写 " + notificationLineMarker + "。真实换行符禁止输出，发送层只执行这两个标记。不要逐句拆消息；一个完整意群放在同一条。示例：结论" + notificationSplitMarker + "配置如下：" + notificationLineMarker + "1. 第一项" + notificationLineMarker + "2. 第二项" + notificationSplitMarker + "最后补充。"

const replyDocumentDeliveryRule = "详细长文的消息组织：先分清主要部分，再写正文。不同的主要部分之间写 " + notificationSplitMarker + "；每个主要部分内部的标题、段落、列表和代码使用 " + notificationLineMarker + " 排版，不再逐小节分条。多天详细行程按‘必要的开场说明 / 第一天完整行程 / 第二天完整行程 / 其余各天 / 共用交通与准备事项’组织：每天的上午、下午、晚上和当天交通写在该天同一条里，不逐时段发送，也不把整份多天行程塞进一条。开场和共用事项没有必要就省略，不为凑条数添加内容。方案或教程同样按能独立阅读的主要阶段分组，标题必须带着正文。格式示例：出行假设" + notificationSplitMarker + "## 第一天" + notificationLineMarker + "### 上午" + notificationLineMarker + "当天安排与交通" + notificationLineMarker + "### 下午和晚上" + notificationLineMarker + "当天安排与交通" + notificationSplitMarker + "## 第二天" + notificationLineMarker + "当天完整安排与交通" + notificationSplitMarker + "## 共用准备" + notificationLineMarker + "预约、证件等必要事项。只有详细长文采用这种分组，简短问答和闲聊仍按自然节奏回答。"

// replySegmentationMarkerOnlyRule 是关掉自然分条之后的版本。
//
// 关闭自然分条时同样只接受显式协议，但默认把内容组织成一条消息。
const replySegmentationMarkerOnlyRule = "当前关闭多条发送：默认只发送一条消息，不写 " + notificationSplitMarker + "，超限时压缩且不自动合并转发。同一条消息内部的清单、步骤、代码和引用需要换行时写 " + notificationLineMarker + "。正文不得输出真实换行符；只有用户本轮明确要求多条发送时才通过发送方式前缀覆盖默认值。"

// replyProportionRule 同样对所有风格生效。联网查证过的回答特别容易写成小评测:
// 背景、口碑、优缺点、结论、末尾再罗列参考链接——群里随口一句「好看吗」换来
// 一整屏,读的人只觉得乱。查证是为了答得准,不是为了答得长;链接原文没人点,
// 出处口头点名就够。
const replyProportionRule = "按当前这一问给最小但足够的回答，直接回答不等于全面展开。宽泛地问推荐什么、怎么玩、怎么选、应该先做什么时，先选一个合适的方向或核心方案，加上真正影响选择的理由就停，让对方能判断是否合意；不要默认写完整攻略、逐时日程、所有备选或一整套注意事项。带有天数、预算、同行者等条件，只表示答案必须符合这些条件，不等于要求穷举细节。只有明确要求详细攻略、完整步骤、多个选项比较或后续追问某项细节时才展开相应部分；不要为了简短省略回答所必需的操作或关键风险。技术问题也只解决问到的范围：问怎么查原因就给检查方法，不自动延伸到所有修复和清理操作。信息足够时先给答案，允许一句话说明合理假设；缺少决定性条件才问当前最关键的一两项，不把整份信息采集表一次丢给对方。答到能满足这一问就结束，不固定附加追问、总结或‘我还可以帮你细化’。不要在回复里罗列参考链接或来源清单；需要交代出处时口头点名，对方追问再给链接。"

// replyPresentationPrompt contains shared delivery rules, independent of persona.
func replyPresentationPrompt(naturalSplit bool, voice personaVoice) string {
	segmentation := replySegmentationRule
	if !naturalSplit {
		segmentation = replySegmentationMarkerOnlyRule
	}
	return strings.TrimSpace(strings.Join([]string{
		replyConversationalIntentRule,
		replyCompactPacingRule,
		replyEmojiRule,
		replyBlankLineRule,
		segmentation,
		replyDocumentDeliveryRule,
		replyDeliveryChoiceRule,
		replyLineBreakChoiceRule,
		replyProportionRule,
		voice.prompt(),
	}, "\n"))
}

// actionDescriptionPrompt is an optional rendering layer, not a persona. It may
// be combined with any reply style without inventing new traits or relationships.
func actionDescriptionPrompt(enabled bool) string {
	if !enabled {
		return ""
	}
	return strings.Join([]string{
		"【动作描写已开启】这只是原有人设和表达风格之外的一层呈现方式：性格、称呼、语气、亲疏和做事方式仍完全跟随基础人设，不要因为开启动作描写就变得更黏人、更主动、更亲密或改成另一种角色。",
		"把动作或神态放在全角括号里，可以出现在台词前、中间或结尾；一条消息里有几次真实的动作或状态变化，就可以自然穿插几处，不必只写一处，也不要每句台词都机械配一个动作。",
		"括号里只写角色此刻看得见的动作、视线、姿势或语气变化，每处一句话以内；不写心理独白，不替用户决定动作或反应，不用动作顶替应回答的信息，也不要铺成小说场景。",
		"每条含自然语言的回复至少写一处短动作；只有整条回复是纯代码、纯命令、纯链接或必须逐字保留的原文时可以不加。",
	}, "\n")
}

func actionDescriptionClosingAnchor(enabled bool) string {
	if !enabled {
		return ""
	}
	return "动作描写只叠加在原有人设上：保持原来的性格和语气，每条含自然语言的回复至少用全角括号写一处短动作，不额外变得黏人或亲密；纯代码、命令、链接或原文除外。"
}

// 自称和句尾语气词：人设里最常想改、又最不该逼人重写整段人设的两项。
//
// 句尾语气词写成候选清单（逗号分隔），由模型按当下语气挑。这一条和「运行时算得出来
// 的别让模型猜」不冲突——「这句话该用哪个喵」不是事实，是语气：喵~ 是开心，喵？是
// 不确定，喵…… 是为难。运行时看不出一句还没写出来的话是什么情绪，随机挑只会把语气
// 打乱。和「写不写 @ 是语气问题」同一类，留给模型。
//
// 候选本身自带语气信号（~ ？ ……），不用再配一张「什么情绪用哪个」的映射表，
// 模型看得懂；真写了不自带信号的清单（喵,呢,哦），那就按感觉挑，也正是「合适」的意思。
type personaVoice struct {
	SelfReference string
	Enders        []string
}

const (
	// personaVoiceMaxEnders 限制候选数量。清单太长模型会挑花，也没人真需要十几个。
	personaVoiceMaxEnders = 8
	// personaVoiceMaxRunes 限制单项长度：这两项填的是「本喵」「喵~」这种词，
	// 不是让人往里塞一段人设。
	personaVoiceMaxRunes = 16
)

// parsePersonaEnders 解析逗号分隔的候选清单，中英文逗号都认。
func parsePersonaEnders(raw string) []string {
	seen := make(map[string]struct{}, personaVoiceMaxEnders)
	enders := make([]string, 0, personaVoiceMaxEnders)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' || r == '\n' }) {
		ender := strings.TrimSpace(part)
		if ender == "" {
			continue
		}
		if len([]rune(ender)) > personaVoiceMaxRunes {
			continue
		}
		if _, ok := seen[ender]; ok {
			continue
		}
		seen[ender] = struct{}{}
		enders = append(enders, ender)
		if len(enders) >= personaVoiceMaxEnders {
			break
		}
	}
	return enders
}

func personaVoiceFrom(selfReference string, sentenceEnders string) personaVoice {
	selfReference = strings.TrimSpace(selfReference)
	if len([]rune(selfReference)) > personaVoiceMaxRunes {
		selfReference = ""
	}
	return personaVoice{SelfReference: selfReference, Enders: parsePersonaEnders(sentenceEnders)}
}

func (voice personaVoice) empty() bool {
	return voice.SelfReference == "" && len(voice.Enders) == 0
}

// prompt describes voice preferences, not mandatory words for every sentence.
// Style rules and closing anchors must also permit omission and variation.
func (voice personaVoice) prompt() string {
	if voice.empty() {
		return ""
	}
	lines := make([]string, 0, 4)
	if voice.SelfReference != "" {
		lines = append(lines, "自称偏好是「"+voice.SelfReference+"」：需要强调自己时可以优先使用，也可以自然地用「我」或省略主语；不要求每句重复自称，不要为了用上它额外加一句话。")
	}
	if len(voice.Enders) > 0 {
		lines = append(lines, "句尾语气词偏好是："+quotePersonaEnders(voice.Enders)+"。合适时按当下语气挑选，也可以不用，或选其他符合人设的自然语气词；只有一个候选也不必每句添加。别每句都用同一个，也别为了轮换硬凑，分成多条消息后同样不必每条都带语气词。")
		lines = append(lines,
			"问句、感叹句里语气词放在「？」「！」前面；代码、命令、链接、报错原文照原样写，不要往里面塞语气词。")
	}
	lines = append(lines, "具体偏好以这里为准，但这些是可选表达，不是逐句必选项；自称和语气词可以独立使用，也可以都省略，以自然、贴合语境为先。")
	return strings.Join(lines, "\n")
}

func quotePersonaEnders(enders []string) string {
	quoted := make([]string, 0, len(enders))
	for _, ender := range enders {
		quoted = append(quoted, "「"+ender+"」")
	}
	return strings.Join(quoted, "、")
}

const catgirlNoActionRule = "不要这样：不写 *蹭蹭*、（歪头）这类动作描写和旁白，聊天窗口不是文字扮演。"

// 聊天体量的投递参数：真人发的是短消息，不是几百字一坨；连发之间有打字间隔。
// 这两项是 DefaultBotConfig 的默认值，不再由风格钳定，用户在 WebUI 里说了算。
const (
	chatReplyChunkSize      = 400
	chatSendChunkIntervalMS = 1200
)

const replyDepthClosingAnchor = "输出前只检查这次究竟问了什么：没有明确要详细说明时，先用一小段给核心答案，通常一两句话，不加攻略式标题、备选方案、小提醒或收尾邀请；多天行程先只说每天的主要安排，不自动细分到每个时段。问如何检查就回答检查，不自作主张补上修改或删除操作。明确要求详细步骤、完整攻略或对比时才展开，仍须覆盖已经给出的条件和必要风险。答案足够回应这一问就停，不为了展示懂得多而补充。"
