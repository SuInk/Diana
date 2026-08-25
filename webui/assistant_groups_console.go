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
func (h *BotHandler) registerConsoleGroupRoutes(router gin.IRouter) {
	router.GET("/api/assistant/groups", h.listConsoleGroups)
	router.POST("/api/assistant/groups", h.saveConsoleGroup)
	router.GET("/api/assistant/groups/:id/relations", h.groupRelationGraph)
}

// groupRelationGraph 返回以机器人为中心的群聊关系图。
func (h *BotHandler) groupRelationGraph(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "消息存储未配置"})
		return
	}
	groupID := strings.TrimSpace(c.Param("id"))
	if groupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少群号"})
		return
	}
	rangeID := strings.TrimSpace(c.DefaultQuery("range", "7d"))
	since, ok := assistantEventsSince(rangeID, time.Now())
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "range 仅支持 1h、24h、7d、30d、all"})
		return
	}
	// 中心节点优先用配置里的机器人账号：新群可能还没有机器人自己的发言，
	// 光靠扫历史找不出中心，图就散成一堆互不相干的点。
	botID := strings.TrimSpace(h.runtime.Config().BotAccount)
	graph, err := h.sqlite.GroupRelationGraphFor(c.Request.Context(), groupID, since, botID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"range": rangeID, "graph": graph})
}

// listConsoleGroups 返回机器人已加入的群、已保存群配置与插件清单。
func (h *BotHandler) listConsoleGroups(c *gin.Context) {
	base := h.runtime.Config()
	profileID := botProfileScope(c)
	set := assistant.GroupConfigSet{Groups: h.groupConfigs.Groups().GroupsForProfile(profileID)}
	refresh := queryBool(c.Query("refresh"))
	liveGroups, liveAvailable, warning := h.consoleGroupSources(c.Request.Context(), profileID, refresh)
	groups := mergeConsoleGroupItems(base, set, liveGroups)
	for index := range groups {
		groups[index].BotProfileID = profileID
	}
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

// consoleGroupSources 按当前作用域决定群列表从哪来。
//
// OneBot 有 get_group_list，问一次就有权威结果。Telegram 的 Bot API 没有「列出我
// 加入的群」这种接口——机器人只有在群里收到过消息才知道自己在那儿，所以只能从
// 本地事件历史里聚合。选了「全部机器人」时沿用原来的行为，避免把两个平台的群混
// 成一份看不出归属的清单。
func (h *BotHandler) consoleGroupSources(ctx context.Context, profileID string, refresh bool) ([]botAutoGroupInfo, bool, string) {
	if profileID == "" || h.isOneBotProfile(profileID) {
		return h.liveConsoleGroups(ctx, refresh)
	}
	if h.sqlite == nil {
		return nil, false, "当前存储不支持按机器人列出群，暂时只显示已保存的群配置"
	}
	seen, err := h.sqlite.ListInboundEventGroups(ctx, time.Now().Add(-consoleLocalGroupWindow), profileID)
	if err != nil || len(seen) == 0 {
		return nil, false, "这台机器人还没有在任何群里收到过消息，暂时只显示已保存的群配置"
	}
	groups := make([]botAutoGroupInfo, 0, len(seen))
	for _, item := range seen {
		groups = append(groups, botAutoGroupInfo{GroupID: item.GroupID})
	}
	return groups, true, ""
}

// isOneBotProfile 判断这台机器人是不是 OneBot 平台。
func (h *BotHandler) isOneBotProfile(profileID string) bool {
	if h.profiles == nil {
		return true
	}
	for _, profile := range h.profiles.Profiles().Profiles {
		if strings.TrimSpace(profile.ID) == profileID {
			return assistant.IsOneBotPlatform(profile.Platform)
		}
	}
	return true
}

var (
	// consoleLocalGroupWindow 是「从本地事件推断在哪些群」的回看窗口。太短会漏掉
	// 冷群，太长会把早就退出的群一直挂在列表上。
	consoleLocalGroupWindow = 30 * 24 * time.Hour

	consoleLiveGroupTimeout  = 2500 * time.Millisecond
	consoleLiveGroupCacheTTL = 20 * time.Second
)

type liveGroupListCache struct {
	groups    []botAutoGroupInfo
	available bool
	warning   string
	fetchedAt time.Time
}

func cloneLiveGroups(groups []botAutoGroupInfo) []botAutoGroupInfo {
	if len(groups) == 0 {
		return nil
	}
	out := make([]botAutoGroupInfo, len(groups))
	copy(out, groups)
	return out
}

func (h *BotHandler) liveConsoleGroups(ctx context.Context, refresh bool) ([]botAutoGroupInfo, bool, string) {
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

func mergeConsoleGroupItems(base assistant.BotConfig, set assistant.GroupConfigSet, liveGroups []botAutoGroupInfo) []consoleGroupItem {
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
			AvatarURL:      assistant.OneBotGroupAvatarURL(groupID),
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
			AvatarURL:   assistant.OneBotGroupAvatarURL(groupID),
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
func (h *BotHandler) saveConsoleGroup(c *gin.Context) {
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
	// 群配置按机器人各存一份，保存时必须钉住是给哪一台配的。
	cfg.BotProfileID = strings.TrimSpace(payload.Config.BotProfileID)
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
