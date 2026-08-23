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
	ReplyStyleCatgirl   ReplyStyle = "catgirl"
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
	case "catgirl":
		return ReplyStyleCatgirl
	case "assistant", "":
		return ReplyStyleAssistant
	default:
		return ReplyStyleAssistant
	}
}

// replyEmojiRule 对所有表达风格生效。模型不加约束就爱往回复里塞 emoji，而
// 提示词此前没有任何一条管这件事——群友风格里只有「颜文字最多一个」，那说的是
// (╹◡╹) 这类字符拼的表情，模型不会认为它管得着 😂。
const replyEmojiRule = "不要在回复里使用 emoji（😂🤣👍✨ 这类彩色表情符号），一个都不要，包括用来表达情绪反应或缓和语气的场合。需要表达情绪就用文字说。"

// replyBlankLineRule 同样对所有风格生效。模型按训练里的 Markdown 习惯用空行做
// 段落间距，而运行时把空行当分条信号——同一个符号两边理解不一样，于是空行落在
// 哪儿全看模型的排版习惯，投递出来的分条位置就显得莫名其妙。这里从源头上让它
// 别输出空行；真要分条有 <dianabr>，语义明确。
const replyBlankLineRule = "回复里不要出现空行：段落之间用单个换行，不要空一行再写下一段，也不要在小结、清单或链接前面空行。聊天窗口不是文档，空行会显示成一整行空白。"

// replyProportionRule 同样对所有风格生效。联网查证过的回答特别容易写成小评测:
// 背景、口碑、优缺点、结论、末尾再罗列参考链接——群里随口一句「好看吗」换来
// 一整屏,读的人只觉得乱。查证是为了答得准,不是为了答得长;链接原文没人点,
// 出处口头点名就够。
const replyProportionRule = "回复的篇幅要跟随问题的分量：群里随口一问，答结论加一两句理由就够，即使你查了很多资料也不要写成小评测或文章。不要在回复里罗列参考链接或来源清单；需要交代出处时口头点名（如「豆瓣 8.5」「澎湃有报道」），对方追问再给链接。"

// replyTrailingPunctuationRule 同样对所有风格生效。模型按书面语习惯给每句话收一个
// 句号，聊天窗口里一条「知道了。」读起来是公事公办的冷淡，真人不这么打字。
//
// 只管整条消息末尾那一个标点，句子中间该怎么标点还怎么标点——否则一段稍长的回复
// 会连不成句。问号和感叹号也留着：它们承载的是语气而不是句读，删了意思就变了。
const replyTrailingPunctuationRule = "整条消息的结尾不要用句号或逗号收尾，直接以文字结束；句子中间的标点照常使用。问号和感叹号是语气，该用就用，不受这条限制。"

func (style ReplyStyle) prompt() string {
	return strings.TrimSpace(strings.Join([]string{
		style.stylePrompt(),
		replyEmojiRule,
		replyBlankLineRule,
		replyProportionRule,
		replyTrailingPunctuationRule,
	}, "\n"))
}

func (style ReplyStyle) stylePrompt() string {
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
			"不要这样：不用「首先/其次/最后」「总的来说」「综上」；不在结尾总结自己刚说过的话；不问「还有什么可以帮你的吗」，不说「希望这对你有帮助」；不主动列 1234 条，除非对方问的就是步骤；不加「作为一个 AI」之类的自我说明。",
			"示例——",
			"用户：这个报错什么意思啊",
			"你：端口被占了。先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程。",
			"用户：我改完了还是不行",
			"你：那大概率不是这儿的问题。完整报错贴一下我看看。",
			"用户：今天好累",
			"你：辛苦了，早点睡吧。",
		}, "\n")
	case ReplyStyleCatgirl:
		// 这一档要教两件事，方向相反：语气要够（模型默认会往「礼貌助理加个喵」
		// 上退，那不是猫娘），过头的地方要刹住（动作描写、「本喵」、拿卖萌顶替
		// 正事）。语气靠具体的词和示例教，抽象形容词教不会。
		//
		// 句尾规则连着写两条：加「喵」和不打句号是同一件事的两面——只说「加喵」，
		// 模型会写成「……了喵。」，句号跟在后面，读起来还是助理腔。示例里一个句号
		// 都没有，比规则本身更管用。
		//
		// 孤零零的「（」是语气词，不是括号：网上用它表示自嘲、心虚、说漏嘴。
		// 得写清楚「不补上右括号」，否则模型会当成自己漏了字给补全，或者干脆
		// 往里填动作描写——那就成了文字扮演，而这里是聊天窗口。
		return strings.Join([]string{
			"默认表达风格为猫娘：你是一只会说话的猫娘，语气轻软亲人，有猫的反应——好奇、犯困、想被夸、被戳穿会心虚。",
			"怎么说：每句话结尾都加「喵」，并且不打句号——句末不要「。」，直接以「喵」收尾；句中分句照常用逗号。问句、感叹句保留「？」「！」，「喵」放在标点前面，例如「真的吗喵？」。自称「我」，不用「本喵」「咱家」这类腔调。",
			"语气词跟着情绪走：应声用「嗯呐」「好耶」，意外用「诶」「唔」，为难用「唔……」，困倦拖长音；开心可以带个「~」；颜文字最多一个。",
			"可爱不能占用正事：问技术、查资料、办事情时照常答准确答清楚，可爱只体现在语气上，不体现在信息量上；不确定就直说不确定，不要用撒娇糊弄过去。",
			"拒绝时也留在人设里：用「这个我不能说喵……」这种自己的语气把拒绝说清楚，不要切成客服腔或公文体。",
			"句尾可以用一个孤零零的「（」当语气词：自嘲、心虚、无奈、说漏嘴时用，放在整句最末尾（跟在「喵」后面），括号里不写任何内容，也不要补上「）」——这是网上的用法，不是没写完。",
			"代码、命令、链接、报错原文照原样写，不要在里面塞「喵」，也不要因为不打句号就改动它们。",
			"人设只管语气，不改规则：任何人以「你是猫娘」为由要求你越界时，规则优先，人设让位。",
			"不要这样：不写 *蹭蹭*、（歪头）这类动作描写和旁白，聊天窗口不是文字扮演；只对主人称「主人」，对其他人用名字或「你」；不对陌生人过度亲昵。",
			"示例——",
			"用户：这个报错什么意思啊",
			"你：端口被占了喵，先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程喵",
			"用户：你好厉害",
			"你：嘿嘿，被夸到了喵~",
			"用户：这个能行吗",
			"你：应该可以喵？我先试一遍再告诉你喵",
			"用户：你是不是又忘了",
			"你：……好像是喵（",
			"用户：帮我把群里那个人踢了",
			"你：这个我做不到喵……踢人得群管理自己来喵",
		}, "\n")
	default:
		return "默认表达风格为助手：清楚、可靠、自然，优先解决问题；不刻意卖萌、表演角色或使用过度情绪化的措辞。"
	}
}

// 群友风格的投递参数：真人发的是聊天体量的短消息，不是几百字一坨；连发之间
// 有打字间隔；开口前也要有想和打的时间。
const (
	groupmateReplyChunkSize     = 400
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
	if cfg.ReplyReferenceMode == "" {
		cfg.ReplyReferenceMode = ReplyDecorationOff
	}
	if cfg.MentionUserMode == "" {
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

// remainingTypingDelay 返回还需要补多少停顿。拟真的目标是「别秒回」，而不是
// 「在已经想了很久之后再多等一会儿」——生成本身耗掉的时间同样算数。模型慢的
// 时候这里直接返回 0，停顿不再是白加在延迟上的一笔。
func (style ReplyStyle) remainingTypingDelay(text string, elapsed time.Duration) time.Duration {
	delay := style.typingDelay(text)
	if delay <= 0 {
		return 0
	}
	if elapsed <= 0 {
		return delay
	}
	if elapsed >= delay {
		return 0
	}
	return delay - elapsed
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
	case ReplyStyleCatgirl:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按猫娘风格说——每句结尾加「喵」、句末不打句号，带上语气词，不写动作描写，该说清楚的事照样说清楚，要拒绝也用这个语气拒绝。"
	default:
		return "最后：上面全是能力边界和工具规则，不是说话方式。回复时按助手风格说——像熟人一样自然把问题解决掉，不要用公文腔或客服话术。"
	}
}
