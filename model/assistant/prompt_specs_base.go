// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// 默认文案在 types.go，这里只登记。

var promptChineseSlangSpec = registerPrompt(PromptSpec{
	Key:     "reply.chinese_slang",
	Group:   PromptGroupReplyBase,
	Title:   "中文梗与修辞",
	Usage:   "「中文梗提示」开关打开时，紧跟在人设后面。",
	Default: defaultPromptChineseSlang,
})

var promptPlaintextRulesSpec = registerPrompt(PromptSpec{
	Key:     "reply.plaintext_rules",
	Group:   PromptGroupReplyBase,
	Title:   "纯文本排版规则",
	Usage:   "平台不渲染 Markdown、本轮会降级成纯文本时注入。支持富文本的平台改发平台说明，不用这段。",
	Default: defaultPromptPlaintextRules,
})

var promptTimeTemplateSpec = registerPrompt(PromptSpec{
	Key:     "reply.time_template",
	Group:   PromptGroupReplyBase,
	Title:   "当前时间",
	Usage:   "「注入时间」打开时，作为尾部实时时钟的第一行。",
	Default: defaultPromptTimeTemplate,
	Vars: []PromptVar{
		{Name: "datetime", Description: "机器人所在机器的当前时间，如 2026-09-23 14:05:00"},
		{Name: "weekday", Description: "中文星期，如 星期三"},
	},
})

var promptGroupSenderSpec = registerPrompt(PromptSpec{
	Key:     "reply.group_sender",
	Group:   PromptGroupReplyBase,
	Title:   "群聊发言者",
	Usage:   "群聊里「注入发言者」打开时，放在历史之后，告诉模型这一轮是谁在说话。",
	Default: defaultPromptGroupSenderTemplate,
	Vars: []PromptVar{
		{Name: "sender", Description: "当前发言者的昵称和用户 ID"},
	},
})

var promptImageOnlySpec = registerPrompt(PromptSpec{
	Key:     "reply.image_only",
	Group:   PromptGroupReplyBase,
	Title:   "只发图片时的正文",
	Usage:   "用户 @ 机器人只发了一张图、没写字时，用这句代替用户正文。",
	Default: defaultPromptImageOnly,
})

var promptWakeOnlySpec = registerPrompt(PromptSpec{
	Key:     "reply.wake_only",
	Group:   PromptGroupReplyBase,
	Title:   "只叫一声时的指引",
	Usage:   "用户只 @ 了机器人或喊了名字、没说别的时附上，告诉模型别只回「在呢」。",
	Default: defaultPromptWakeOnly,
})

var promptProactiveReplySpec = registerPrompt(PromptSpec{
	Key:     "reply.proactive_reply",
	Group:   PromptGroupReplyBase,
	Title:   "主动回复说明",
	Usage:   "意图识别判定要主动回复某条消息时，拼在正式回复的系统提示词里。",
	Default: defaultProactiveReplyPrompt,
})

var promptLegacyRouterSpec = registerPrompt(PromptSpec{
	Key:     "routing.legacy_router",
	Group:   PromptGroupRouting,
	Title:   "旧版意图路由",
	Usage:   "只在没有启用「参与度」评分的旧配置上使用：判断一批群消息里有没有该回的、回哪条。启用参与度后改用接话评分，这段不再发送。",
	Default: defaultProactiveReplyRouterPrompt,
})
