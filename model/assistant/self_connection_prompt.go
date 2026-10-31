// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// promptSelfConnectionSpec 告诉模型这台机器人自己是怎么接进聊天平台的。
//
// 10-31 一台 QQ 官方机器人被问「你是怎么跑起来的」，引用的上一条里提到 SnowLuma，
// 命中了 core:qq-onebot-client 这条部署推荐，它就说自己靠 SnowLuma 反向 WebSocket
// 连 QQ，被群友指出是官方机器人还坚持说不是。提示词里原本没有一处说它在哪个平台，
// 换个问法触发词也兜不住，所以常驻在稳定头部：每台机器人的平台固定，不破坏缓存。
//
// 注入的是「知道」不是「要说」：想藏身份的人设照样可以不答，只是不能编一个假的。
var promptSelfConnectionSpec = registerPrompt(PromptSpec{
	Key:     "reply.self_connection",
	Group:   PromptGroupReplyBase,
	Title:   "自己的接入方式",
	Usage:   "每轮都注入：按机器人所在平台说明它自己怎么接进来的，免得把部署推荐或聊天记录里别人的接法当成自己的。",
	Default: "关于你自己：{connection}这是你自己的事实，别人没问不用主动提；人设要求不透露身份或技术细节时，按人设含糊带过或不答，但不要编一个别的接入方式，也不要把部署推荐、聊天记录里别人说的接法当成你自己的。",
	Vars:    []PromptVar{{Name: "connection", Description: "按平台生成的一句接入方式说明"}},
})

// selfConnectionPromptForConfig 生成常驻的接入方式说明，平台认不出时不注入。
func selfConnectionPromptForConfig(cfg BotConfig) string {
	connection := selfConnectionFact(NormalizePlatformID(cfg.Platform))
	if connection == "" {
		return ""
	}
	return cfg.promptf(promptSelfConnectionSpec, map[string]string{"connection": connection})
}

// selfConnectionFact 按平台说明这台机器人自己的接入方式。面向模型的文字只点名
// SnowLuma，不提别的 OneBot 客户端。
func selfConnectionFact(platform string) string {
	if strings.TrimSpace(platform) == "" {
		return ""
	}
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11:
		return "你这台机器人是 QQ 个人号，经 OneBot v11 协议端（推荐 SnowLuma）和 Diana 连接，具体是正向 WebSocket、反向 WebSocket 还是 HTTP 看部署时的配置。"
	case PlatformQQOfficial:
		return "你这台机器人是 QQ 官方机器人：Diana 用开放平台的 AppID 和密钥，通过 QQ 开放平台的 WebSocket 网关收发消息（出站长连接，不需要公网地址），不经过 SnowLuma 或任何 OneBot 协议端，也没有登录 QQ 个人号。"
	}
	def, ok := PlatformByID(platform)
	if !ok {
		return ""
	}
	return "你这台机器人在" + def.Name + "上：" + def.Description + "，不经过 QQ 的 OneBot 协议端。"
}
