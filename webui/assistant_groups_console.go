// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 控制台群管理接口：走全局登录鉴权，登录用户即管理员，
// 无需再经过登录验证码流程（该流程保留给群管自助场景）。

type consoleGroupsResponse struct {
	Groups        []consoleGroupItem      `json:"groups"`
	Plugins       []assistant.PluginState `json:"plugins"`
	LiveAvailable bool                    `json:"live_available"`
	Warning       string                  `json:"warning,omitempty"`
}

type consoleGroupItem struct {
	assistant.GroupConfig
	GroupName      string `json:"group_name,omitempty"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	MemberCount    int    `json:"member_count,omitempty"`
	MaxMemberCount int    `json:"max_member_count,omitempty"`
	Configured     bool   `json:"configured"`
	Joined         bool   `json:"joined"`
}

type consoleGroupSavePayload struct {
	Config assistant.GroupConfig `json:"config"`
}

// registerConsoleGroupRoutes 注册控制台群配置直连路由。
func (h *QQBotHandler) registerConsoleGroupRoutes(router gin.IRouter) {
	for _, base := range []string{"/api/assistant", "/api/qqbot"} {
		router.GET(base+"/groups", h.listConsoleGroups)
		router.POST(base+"/groups", h.saveConsoleGroup)
	}
}

// listConsoleGroups 返回机器人已加入的群、已保存群配置与插件清单。
func (h *QQBotHandler) listConsoleGroups(c *gin.Context) {
	base := h.runtime.Config()
	set := h.groupConfigs.Groups()
	refresh := queryBool(c.Query("refresh"))
	liveGroups, liveAvailable, warning := h.liveConsoleGroups(c.Request.Context(), refresh)
	groups := mergeConsoleGroupItems(base, set, liveGroups)
	for index := range groups {
		groups[index].GroupConfig = h.groupConfigForAPI(groups[index].GroupConfig)
	}
	c.JSON(http.StatusOK, consoleGroupsResponse{
		Groups:        groups,
		Plugins:       assistant.RedactStates(h.runtime.Plugins().ListVisible()),
		LiveAvailable: liveAvailable,
		Warning:       warning,
	})
}

var (
	consoleLiveGroupTimeout  = 2500 * time.Millisecond
	consoleLiveGroupCacheTTL = 20 * time.Second
)

type liveGroupListCache struct {
	groups    []qqbotAutoGroupInfo
	available bool
	warning   string
	fetchedAt time.Time
}

func cloneLiveGroups(groups []qqbotAutoGroupInfo) []qqbotAutoGroupInfo {
	if len(groups) == 0 {
		return nil
	}
	out := make([]qqbotAutoGroupInfo, len(groups))
	copy(out, groups)
	return out
}

func (h *QQBotHandler) liveConsoleGroups(ctx context.Context, refresh bool) ([]qqbotAutoGroupInfo, bool, string) {
	if h == nil {
		return nil, false, "机器人尚未连接，暂时只显示已保存的群配置"
	}
	if !refresh {
		h.liveGroupMu.Lock()
		cached := h.liveGroupCache
		h.liveGroupMu.Unlock()
		if !cached.fetchedAt.IsZero() && time.Since(cached.fetchedAt) < consoleLiveGroupCacheTTL {
			return cloneLiveGroups(cached.groups), cached.available, cached.warning
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, consoleLiveGroupTimeout)
	defer cancel()
	params := map[string]any{}
	if refresh {
		params["no_cache"] = true
	}
	data, err := h.runtime.CallOneBotAPI(callCtx, "get_group_list", params)
	if err != nil {
		warning := "机器人尚未连接，暂时只显示已保存的群配置"
		if callCtx.Err() != nil {
			warning = "同步群列表超时，暂时只显示已保存的群配置"
		}
		return nil, false, warning
	}
	liveGroups := autoGroupsFromOneBotData(data)
	h.liveGroupMu.Lock()
	h.liveGroupCache = liveGroupListCache{
		groups:    cloneLiveGroups(liveGroups),
		available: true,
		warning:   "",
		fetchedAt: time.Now(),
	}
	h.liveGroupMu.Unlock()
	return liveGroups, true, ""
}

func mergeConsoleGroupItems(base assistant.BotConfig, set assistant.GroupConfigSet, liveGroups []qqbotAutoGroupInfo) []consoleGroupItem {
	saved := make(map[string]assistant.GroupConfig, len(set.Groups))
	for _, cfg := range set.Groups {
		groupID := strings.TrimSpace(cfg.GroupID)
		if groupID != "" {
			saved[groupID] = cfg.WithDefaults(groupID, base)
		}
	}

	items := make([]consoleGroupItem, 0, len(liveGroups)+len(saved))
	seen := make(map[string]struct{}, len(liveGroups))
	for _, live := range liveGroups {
		groupID := strings.TrimSpace(live.GroupID)
		if groupID == "" {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		cfg, configured := saved[groupID]
		if !configured {
			cfg = assistant.DefaultGroupConfig(groupID, base)
		}
		items = append(items, consoleGroupItem{
			GroupConfig:    cfg.WithDefaults(groupID, base),
			GroupName:      strings.TrimSpace(live.GroupName),
			AvatarURL:      assistant.QQGroupAvatarURL(groupID),
			MemberCount:    live.MemberCount,
			MaxMemberCount: live.MaxMemberCount,
			Configured:     configured,
			Joined:         true,
		})
		delete(saved, groupID)
	}
	for groupID, cfg := range saved {
		items = append(items, consoleGroupItem{
			GroupConfig: cfg,
			AvatarURL:   assistant.QQGroupAvatarURL(groupID),
			Configured:  true,
			Joined:      false,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Joined != items[j].Joined {
			return items[i].Joined
		}
		leftName := strings.ToLower(strings.TrimSpace(items[i].GroupName))
		rightName := strings.ToLower(strings.TrimSpace(items[j].GroupName))
		if leftName != rightName {
			if leftName == "" {
				return false
			}
			if rightName == "" {
				return true
			}
			return leftName < rightName
		}
		return items[i].GroupID < items[j].GroupID
	})
	return items
}

// saveConsoleGroup 创建或更新单个群配置。
func (h *QQBotHandler) saveConsoleGroup(c *gin.Context) {
	var payload consoleGroupSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, "", nil)
		return
	}
	groupID := strings.TrimSpace(payload.Config.GroupID)
	if _, err := strconv.ParseInt(groupID, 10, 64); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", fmt.Errorf("群号格式不正确"), groupID, nil)
		return
	}
	cfg, err := h.sanitizeGroupConfigPayload(payload.Config, groupID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	saved, err := h.groupConfigs.SaveGroupConfig(cfg, h.runtime.Config())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.groups.save", "群配置已保存（控制台）", groupID, map[string]any{"group_id": groupID})
	c.JSON(http.StatusOK, gin.H{"config": h.groupConfigForAPI(saved.WithDefaults(groupID, h.runtime.Config()))})
}
