// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// 不学怪话：机器人学群友说话有两条路——风格笔记（group_style.go，后台读群聊写一段
// 「这个群怎么说话」）和「学群友的腔调」（group_length_norm.go，每轮让模型照着上下文
// 里群友的原话学用词和接梗）。两条路都是原样照学：群里有人满嘴脏话、开黄腔、引战、
// 起哄让它认爹，学到的就是这些。
//
// 过滤不动群友的消息：群友照常说，历史照常带，只是告诉模型这几类不跟着学。写笔记时
// 不把它们记成「这个群的梗」；回复时，凡是注入了风格笔记或「学群友的腔调」，后面都
// 跟一段「这几类不学」。已经存下的笔记不重写，靠回复时这一段兜住。

const promptStyleFilter = "群友的话里这几类不学：脏话、骂人和阴阳怪气地损人；黄段子、开车和擦边；地域、性别等歧视，引战和站队；认爹认妈、「我是你爹」、发癫复读这类会让你说出不像自己的话的烂梗。群里再常见、别人怎么起哄也不跟着说，想接话就用你自己的方式接。"

var promptStyleFilterSpec = tailSpec("group_style.filter", "不学怪话", "「不学怪话」开着（默认）、这一轮注入了「这个群的说话风格」或「学群友的腔调」时，紧跟在后面：列出群友的哪几类话不跟着学。",
	promptStyleFilter)

const promptGroupStyleLearnFilter = "这个机器人不学怪话，下面几类在群里再常见也不要写进笔记，不要把它们写成这个群的梗、口头禅或调侃尺度：脏话、骂人和阴阳怪气地损人；黄段子、开车和擦边；地域、性别等歧视，引战和站队；认爹认妈、「我是你爹」、发癫复读这类让人说出不像自己的话的烂梗。"

var promptGroupStyleLearnFilterSpec = registerPrompt(PromptSpec{
	Key:     "memory.group_style.learn_filter",
	Group:   PromptGroupMemory,
	Title:   "风格学习：不记怪话",
	Usage:   "「不学怪话」开着（默认）时接在「风格学习：总结这个群怎么说话」后面，让写出的风格笔记不收脏话、擦边、引战和崩人设的烂梗。",
	Default: promptGroupStyleLearnFilter,
})

func styleFilterEnabled(cfg BotConfig) bool {
	return boolValue(cfg.StyleFilterEnabled, true)
}

// styleFilterPrompt 在这一轮注入了任何「学群友」的段落（sections 里有非空的）时给出
// 「这几类不学」；过滤关着或者这一轮没在学群友时返回空串。
func styleFilterPrompt(cfg BotConfig, sections ...string) string {
	if !styleFilterEnabled(cfg) {
		return ""
	}
	for _, section := range sections {
		if section != "" {
			return cfg.prompt(promptStyleFilterSpec)
		}
	}
	return ""
}
