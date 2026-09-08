package assistant

import "strings"

// Historical prompt fixtures keep legacy persona regression coverage separate
// from the runtime, which no longer dispatches on style enums.
func (style ReplyStyle) prompt(naturalSplit bool, voice personaVoice) string {
	return style.promptWithActions(naturalSplit, voice, false)
}

func (style ReplyStyle) promptWithActions(naturalSplit bool, voice personaVoice, actionsEnabled bool) string {
	stylePrompt := style.stylePrompt()
	if actionsEnabled {
		stylePrompt = strings.ReplaceAll(stylePrompt, catgirlNoActionRule+"\n", "")
	}
	return stylePrompt + "\n" + replyPresentationPrompt(naturalSplit, voice)
}

func (style ReplyStyle) closingAnchor() string {
	return style.styleClosingAnchor() + "\n" + replyDepthClosingAnchor
}

func (style ReplyStyle) styleClosingAnchor() string {
	switch style.Normalized() {
	case ReplyStyleGentle:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按温柔风格说——先接住对方的感受，再把话说清楚，别用公文腔。"
	case ReplyStyleLively:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按活泼风格说——语气轻快、有反应感，别用公文腔，也别为了热闹牺牲准确。"
	case ReplyStyleConcise:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按简洁风格说——直接给结论，不铺垫、不复述、不做收尾总结。"
	case ReplyStyleRoleplay:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按扮演风格说——动作可在台词前后用括号自然穿插一处或多处，每处一句以内，别写成小说，正事照样答准。"
	case ReplyStyleHuman:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按真人感风格说——短句自然、情绪外放、有反应感，尽量少发几条，不每条都追问，不强行连发，不写括号动作，不做收尾总结，正事照样答准。"
	case ReplyStyleCatgirl:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按猫娘风格自然地说，具体自称和语气词参考已配置的偏好，大多数句子可以省略，不要逐句重复或机械加后缀，分享近况先给反应不必追问；标点按语义自然使用，不写动作描写，该说清楚的事照样说清楚，要拒绝也保持自己的口吻。"
	default:
		return "最后：能力、安全和回答范围的规则仍然有效，下面只调整口吻。回复时按助手风格说——像熟人一样自然把问题解决掉，不要用公文腔或客服话术。"
	}
}

// DefaultPersonaVoice 返回某个风格自带的自称和句尾候选，供 WebUI 在切换风格时把这两个
// 框填上。填进去而不是运行时暗中套用：填进去用户看得见、能改，暗中套用会得到「框里写着
// A、发出来是 B」这种既看不见又在生效的状态（clearChatInFineTuning 那里踩过同样的坑）。
//
// 留空表示这个风格对自称和句尾没有主张，不是「清空用户填的」。
func DefaultPersonaVoice(style ReplyStyle) (selfReference string, sentenceEnders string) {
	switch style.Normalized() {
	case ReplyStyleCatgirl:
		return "我", "喵,喵~,喵？,喵……"
	case ReplyStyleRoleplay:
		// 扮演对句尾语气词没有主张：那属于具体角色，不属于这套说话方式。
		// 自称写「我」是因为这一档最容易滑成第三人称通篇叙述。
		return "我", ""
	case ReplyStyleHuman:
		// 句尾候选留空是有意的：这一档的语气词要跟着情绪走，钉死一组反而会变成
		// 每句话都挂同一个后缀——那正是它要避开的机械感。
		return "我", ""
	}
	return "", ""
}
