// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"slices"
	"strings"
)

// profileMarkedBotIDs 返回这条消息所在范围内生效的机器人标记。
//
// 范围就是标记被填在哪儿：群配置里那份只在那个群生效，机器人配置里那份对这台
// 机器人的所有会话生效。effectiveConfigForEventLocked（runtime.go:1306）已经在群
// 事件上把本群那份并进 cfg.MarkedBotIDs，所以这里直接用它就是对的。
//
// 这里一度改成跨群汇总——把这台机器人名下每个群标过的账号并成一份到处生效。
// 那样做的代价是标记会串门：在 A 群标下的账号，到 B 群照样被当成机器人抑制，
// 而 B 群的编辑页上两处都看不见它，管理员无从知道为什么不理人。要全局生效就填
// 机器人级那一份，那是明确表达「这个账号在哪儿都是机器人」的地方。
func (r *Runtime) profileMarkedBotIDs(event MessageEvent) []string {
	return cleanStrings(append([]string(nil), r.effectiveConfigForEvent(event).MarkedBotIDs...))
}

// accountMarkedAsBot 判断发信账号是不是被管理员标成了机器人——群聊私聊同一套。
func (r *Runtime) accountMarkedAsBot(event MessageEvent) bool {
	userID := strings.TrimSpace(event.UserID)
	if r == nil || userID == "" {
		return false
	}
	return slices.Contains(r.profileMarkedBotIDs(event), userID)
}
