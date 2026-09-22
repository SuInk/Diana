// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"log"
	"slices"
	"sort"

	"github.com/SuInk/diana/model/assistant"
)

// groupScopeProfiles 和 groupScopeGroups 只要迁移用得到的那几个方法，
// 测试因此可以拿内存实现跑完整条迁移。
type groupScopeProfiles interface {
	Profiles() assistant.ProfileSet
	SaveProfiles(assistant.ProfileSet) error
}

type groupScopeGroups interface {
	Groups() assistant.GroupConfigSet
	SaveGroupConfig(assistant.GroupConfig, assistant.BotConfig) (assistant.GroupConfig, error)
}

// MigrateGroupScopeSwitches 把「这台机器人在这个群工作吗」的三份存储合成一份。
//
// 以前群管理页写群配置的 Enabled、聊天指令写 DisabledGroups、机器人配置页写
// GroupAdmission.AllowedGroups，三份各记一段、两套判据。迁移后逐群开关只剩群
// 配置里那一份，GroupAdmission 只保留「新群默认工不工作」。
//
// 迁移保持行为不变：原来不工作的群迁完仍然不工作。白名单模式下名单外的已知群
// 会被显式关掉，没有群配置的群交给新群默认继续挡着。
//
// 两个名单都空就什么也不做，所以重复执行是安全的：写群配置本身幂等，即使清空
// 名单那一步失败，下次启动会照着同样的输入再算一遍。
func MigrateGroupScopeSwitches(profiles groupScopeProfiles, groups groupScopeGroups) error {
	set := profiles.Profiles()
	changed := false
	for index := range set.Profiles {
		profile := set.Profiles[index].WithDefaults()
		admission := profile.GroupAdmission.WithDefaults()
		allowed, disabled := admission.AllowedGroups, profile.DisabledGroups
		if len(allowed) == 0 && len(disabled) == 0 {
			continue
		}
		whitelist := admission.Mode == assistant.GroupAdmissionWhitelist
		for _, groupID := range migratedGroupIDs(groups.Groups(), profile.ID, allowed, disabled) {
			current, configured := groups.Groups().ConfigForGroup(profile.ID, groupID)
			enabled := !whitelist || slices.Contains(allowed, groupID)
			if configured && !whitelist {
				// 黑名单模式下群配置已经是这个群的开关，保持它自己的值，
				// 只有聊天指令停过的群要被强制关掉。
				enabled = current.WithDefaults(groupID, profile).Enabled
			}
			if slices.Contains(disabled, groupID) {
				enabled = false
			}
			if configured && current.WithDefaults(groupID, profile).Enabled == enabled && current.EnabledSet {
				continue
			}
			if !configured {
				current = assistant.DefaultGroupConfig(groupID, profile)
				current.BotProfileID = profile.ID
			}
			current.Enabled, current.EnabledSet = enabled, true
			if _, err := groups.SaveGroupConfig(current, profile); err != nil {
				return err
			}
		}
		set.Profiles[index].GroupAdmission.AllowedGroups = nil
		set.Profiles[index].DisabledGroups = nil
		changed = true
		log.Printf("diana 机器人「%s」的群开关已合并到群管理：白名单 %d 个群、聊天停用 %d 个群", profile.Name, len(allowed), len(disabled))
	}
	if !changed {
		return nil
	}
	return profiles.SaveProfiles(set)
}

// migratedGroupIDs 是这台机器人要重写开关的群：已经有群配置的，加上两份老名单
// 点到过的。顺序固定，日志和测试才稳定。
func migratedGroupIDs(set assistant.GroupConfigSet, profileID string, lists ...[]string) []string {
	known := map[string]bool{}
	for _, cfg := range set.Groups {
		if cfg.BotProfileID == profileID && cfg.GroupID != "" {
			known[cfg.GroupID] = true
		}
	}
	for _, list := range lists {
		for _, groupID := range list {
			if groupID != "" {
				known[groupID] = true
			}
		}
	}
	ids := make([]string, 0, len(known))
	for groupID := range known {
		ids = append(ids, groupID)
	}
	sort.Strings(ids)
	return ids
}
