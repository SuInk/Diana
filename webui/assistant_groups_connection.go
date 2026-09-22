// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// 复用同一条连接的机器人共用一个平台账号：一条群消息交给每一台，各自按自己的群
// 开关决定回不回。于是「哪个群归哪台」这张路由表散在 N 台各自的配置里，跨机器人
// 看不到全貌——两台同时放行一个群就会双回，而保存时没有任何提示。
//
// 判定链路不动：admitsGroupScope 仍然是唯一判据，这里只是把同一份配置换个看得见
// 的角度读出来，以及在保存时把冲突说出来。投递层不加第二张路由表。

// consoleConnectionPeer 是同一条连接上的另一台机器人，以及它的群归属。
type consoleConnectionPeer struct {
	BotProfileID string `json:"bot_profile_id"`
	Name         string `json:"name,omitempty"`
	// Enabled 是这台机器人本身启不启用。停用的不参与回复，列出来只为说明它在这条连接上。
	Enabled bool `json:"enabled"`
	// NewGroupEnabled 是它的新群默认：true 相当于「所有群都收」，白名单模式才是划分。
	NewGroupEnabled bool `json:"new_group_enabled"`
	// EnabledGroups 是它明确开着的群号。新群默认为开时，这份名单之外的群它也照收。
	EnabledGroups []string `json:"enabled_groups,omitempty"`
}

// connectionIDForProfile 取这台机器人所在的连接。没配复用时连接就是它自己。
func connectionIDForProfile(profile assistant.BotConfig) string {
	if connection := strings.TrimSpace(profile.ConnectionProfileID); connection != "" {
		return connection
	}
	return strings.TrimSpace(profile.ID)
}

// connectionPeers 返回复用同一条连接的其它机器人及其群归属，按机器人名排序。
// 选了「全部机器人」时返回空：那个视图里每个群本来就按机器人各列一遍。
func (h *BotHandler) connectionPeers(profileID string) []consoleConnectionPeer {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || h.profiles == nil {
		return nil
	}
	set := h.profiles.Profiles().WithDefaults()
	connection := ""
	for _, profile := range set.Profiles {
		if profile.ID == profileID {
			connection = connectionIDForProfile(profile)
			break
		}
	}
	if connection == "" {
		return nil
	}
	peers := []consoleConnectionPeer{}
	for _, profile := range set.Profiles {
		if profile.ID == profileID || connectionIDForProfile(profile) != connection {
			continue
		}
		peers = append(peers, consoleConnectionPeer{
			BotProfileID:    profile.ID,
			Name:            profile.Name,
			Enabled:         profile.Enabled,
			NewGroupEnabled: profile.GroupAdmission.NewGroupEnabled(),
			EnabledGroups:   h.explicitlyEnabledGroups(profile),
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Name != peers[j].Name {
			return peers[i].Name < peers[j].Name
		}
		return peers[i].BotProfileID < peers[j].BotProfileID
	})
	if len(peers) == 0 {
		return nil
	}
	return peers
}

// explicitlyEnabledGroups 列出这台机器人明确开着的群。没有群配置的群按新群默认走，
// 不在这份名单里——名单是「点过头的」，默认是另一回事，界面上要分开说。
func (h *BotHandler) explicitlyEnabledGroups(profile assistant.BotConfig) []string {
	if h.groupConfigs == nil {
		return nil
	}
	var groups []string
	for _, cfg := range h.groupConfigs.Groups().GroupsForProfile(profile.ID) {
		groupID := strings.TrimSpace(cfg.GroupID)
		if groupID == "" {
			continue
		}
		if cfg.WithDefaults(groupID, profile).Enabled {
			groups = append(groups, groupID)
		}
	}
	sort.Strings(groups)
	return groups
}

// connectionConflictWarning 说清这次开启的群里，哪几个会同时被同连接的别的机器人
// 接走。只是告警，不阻断保存：确实想让一个群里有多台机器人说话是合法配置。
func (h *BotHandler) connectionConflictWarning(profileID string, groupIDs []string) string {
	var lines []string
	seen := map[string]bool{}
	for _, groupID := range groupIDs {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" || seen[groupID] {
			continue
		}
		seen[groupID] = true
		shared := h.groupSharedBots(profileID, groupID)
		if len(shared) == 0 {
			continue
		}
		names := make([]string, 0, len(shared))
		for _, bot := range shared {
			names = append(names, "「"+firstNonEmptyText(bot.Name, bot.BotProfileID)+"」")
		}
		lines = append(lines, fmt.Sprintf("群 %s 与 %s", groupID, strings.Join(names, "、")))
	}
	if len(lines) == 0 {
		return ""
	}
	// 冲突多的时候不要把整条提示刷成一屏，说清前几个再给个总数。
	const maxListed = 3
	total := len(lines)
	if total > maxListed {
		lines = append(lines[:maxListed], fmt.Sprintf("另有 %d 个群同样如此", total-maxListed))
	}
	return "这条连接上同一个群有多台机器人在回：" + strings.Join(lines, "；") + "。它们共用一个平台账号，群里会看到同一个号连发几条。要只留一台说话，把别的台在这些群关掉。"
}

// connectionDefaultOnWarning 提示「同一条连接上多台都收所有群」这种配置。
//
// 每台的新群默认都开着，等于每台都收所有群，不是某一个群配错了——所以单看某个群
// 的冲突提示不会出现，得单独说，并指个方向：改用「新群默认不工作」再逐群划分。
func (h *BotHandler) connectionDefaultOnWarning(profileID string) string {
	var names []string
	for _, peer := range h.connectionPeers(profileID) {
		if peer.Enabled && peer.NewGroupEnabled {
			names = append(names, "「"+firstNonEmptyText(peer.Name, peer.BotProfileID)+"」")
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "这条连接上 " + strings.Join(names, "、") + " 的新群默认也是工作：几台都收所有群，同一个群里会有多台一起回。想按群分工的话，把它们的新群默认改成不工作，再逐群开给对应的那一台。"
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
