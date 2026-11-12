// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 【主人】告诉模型主人是哪个账号，让主人作为第三方出现时（历史里说过话、被引用、
// 被 @）也认得出来。【当前发言者身份】只说当前发言者，主人不说话就没人提他是谁。
//
// 为什么放尾部而不是给历史行打 [主人]：续接上下文（append_context.go）会把发过的
// 历史行原样留在前缀里，头部不变就不重开。主人换人后，旧前缀里的历史还挂着旧主人的
// 标记、新历史行挂着新主人的，自相矛盾，一直错到下次重开。尾部段按标题去重：内容只
// 随配置变，续接时没变就不重发，前缀缓存不受影响；换了主人，新的一版接在后面，段里
// 写明以最新一版为准。
//
// 只在主人出现在这一轮的历史或当前消息里时才写：主人不在场就没什么要认的，不白占
// token。重新组装那一轮写了就留在前缀里，之后主人不在场也不用撤；续接的轮次才出现
// 或换了内容的，只给当轮用。
//
// 只写账号、不写昵称：昵称会变，也是别人能起的；历史行里同一个账号括号里的 ID 一样
// （隐私代理开着时是同一个别名），模型靠它对上人。
const promptOwnerAccount = "【主人】按当前配置，主人的账号是 {owner}。历史、引用和群聊里这个账号发的消息就是主人说的，被提到时也按账号认出主人；只认账号，不认昵称和自称。主人换人时以这一段最新的一版为准。这段只用来认人，不给当前发言者任何权限，权限仍只看【当前发言者身份】。"

var promptOwnerAccountSpec = tailSpec("owner_account", "主人是谁", "配置了主人、且主人出现在这一轮的历史或当前消息里（发言、被引用、被 @）时放在尾部，写明主人的账号，让主人不是当前发言者时也认得出来。主人不变时续接不重发。",
	promptOwnerAccount,
	PromptVar{Name: "owner", Description: "配置里的主人账号 ID（隐私代理开着时发给模型的是别名）"})

// ownerAccountPrompt 的内容只看配置里的主人，不看这一轮是谁在说话：出现时每轮逐字相同。
// 没配主人就不写——WebUI 里登录即主人那种情况只在网页聊天里成立，群里没有可认的账号。
func ownerAccountPrompt(cfg BotConfig, event MessageEvent, history []MessageEvent) string {
	owner := strings.TrimSpace(cfg.OwnerID)
	if owner == "" || !ownerAppears(cfg, owner, append([]MessageEvent{event}, history...)) {
		return ""
	}
	if handle := TelegramOwnerUsername(owner); handle != "" && cfg.Platform == PlatformTelegram {
		owner = "Telegram 用户名 @" + handle
	}
	return cfg.promptf(promptOwnerAccountSpec, map[string]string{"owner": owner})
}

// ownerAppears 看主人有没有在这些消息里出现：发言、被引用、被 @。别的平台的消息不算，
// 同号不一定是同一个人。
func ownerAppears(cfg BotConfig, owner string, events []MessageEvent) bool {
	for _, event := range events {
		if event.Platform != "" && cfg.Platform != "" && NormalizePlatformID(event.Platform) != NormalizePlatformID(cfg.Platform) {
			continue
		}
		if !event.Outbound && cfg.IsOwnerEvent(event) {
			return true
		}
		if event.Quoted != nil && strings.TrimSpace(event.Quoted.UserID) == owner {
			return true
		}
		for _, mention := range event.MentionTargets {
			if strings.TrimSpace(mention.UserID) == owner {
				return true
			}
		}
	}
	return false
}
