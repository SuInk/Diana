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

// MigrateGroupInheritance 在启动时清掉旧群配置里抄进来的机器人值快照，并立刻落盘。
//
// 读取时也会迁（GroupConfig.WithDefaults），但那只在内存里：迁移按「和机器人现在的值
// 相同」判断快照，用户升级后要是先去机器人页改了值，没落盘的旧快照就再也对不上，
// 成了永久的单独设置。所以要赶在任何人改配置之前，按各群自己那台机器人存一遍。
func MigrateGroupInheritance(profiles groupScopeProfiles, groups groupScopeGroups) error {
	byID := map[string]assistant.BotConfig{}
	for _, profile := range profiles.Profiles().Profiles {
		byID[profile.ID] = profile
	}
	migrated := 0
	for _, cfg := range groups.Groups().Groups {
		if cfg.InheritanceMigrated {
			continue
		}
		// 没有机器人标记的老记录交给群归属迁移，这里拿不准该跟哪台比，宁可不动。
		base, ok := byID[cfg.BotProfileID]
		if !ok {
			continue
		}
		if _, err := groups.SaveGroupConfig(cfg, base); err != nil {
			return err
		}
		migrated++
	}
	if migrated > 0 {
		log.Printf("diana 已把 %d 个群配置里抄进来的机器人设置改回跟随机器人（和机器人现值不同的保留为本群单独设置）", migrated)
	}
	return nil
}
