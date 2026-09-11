// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"slices"
	"strings"
)

// GroupConfigLister 能列出全部群配置。GroupConfigStore 只按群号查得到配置，
// 回答不了「这个账号被标记在哪些群里」——而标记的是账号，不是群里的那个身份：
// 同一个机器人换到私聊就不是机器人了，说不通。
type GroupConfigLister interface {
	Groups() GroupConfigSet
}

// profileMarkedBotIDs 汇总这台机器人能看见的全部机器人标记：机器人级的一份，
// 加上它所在每个群各自标的那些。
//
// effectiveConfigForEventLocked（runtime.go:1293）只在群事件上合并群级标记，
// 所以私聊里 cfg.MarkedBotIDs 只剩机器人级的那一份——线上那份恰好是空的，
// 于是一个在群里被标成机器人的账号，一进私聊就又变回了人。
func (r *Runtime) profileMarkedBotIDs(event MessageEvent) []string {
	cfg := r.effectiveConfigForEvent(event)
	ids := cleanStrings(append([]string(nil), cfg.MarkedBotIDs...))
	r.mu.RLock()
	lister, _ := r.groupConfigs.(GroupConfigLister)
	r.mu.RUnlock()
	if lister == nil {
		return ids
	}
	profileID := strings.TrimSpace(event.ProfileID)
	for _, group := range lister.Groups().Groups {
		// 空 bot_profile_id 是没有分档的旧配置，仍然属于当前这台机器人。
		if owner := strings.TrimSpace(group.BotProfileID); owner != "" && profileID != "" && owner != profileID {
			continue
		}
		ids = append(ids, group.MarkedBotIDs...)
	}
	return cleanStrings(ids)
}

// accountMarkedAsBot 判断发信账号是不是被管理员标成了机器人——群聊私聊同一套。
func (r *Runtime) accountMarkedAsBot(event MessageEvent) bool {
	userID := strings.TrimSpace(event.UserID)
	if r == nil || userID == "" {
		return false
	}
	return slices.Contains(r.profileMarkedBotIDs(event), userID)
}
