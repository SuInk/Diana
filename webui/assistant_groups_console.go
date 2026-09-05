// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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
	router.DELETE("/api/assistant/groups/:id", h.deleteConsoleGroup)
	router.GET("/api/assistant/groups/:id/relations", h.groupRelationGraph)
	router.GET("/api/assistant/groups/:id/avatar", h.groupAvatar)
}

func (h *BotHandler) deleteConsoleGroup(c *gin.Context) {
	profileID, supplied := c.GetQuery("profile")
	if !supplied {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定群配置所属机器人"})
		return
	}
	groupID := strings.TrimSpace(c.Param("id"))
	found, err := h.groupConfigs.DeleteGroupConfig(strings.TrimSpace(profileID), groupID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.groups.delete", err, groupID, nil)
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "群配置不存在"})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.groups.delete", "群配置已删除，恢复全局规则", groupID, map[string]any{"bot_profile_id": profileID})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// groupAvatar 把群头像从平台取回来再转发给控制台。
//
// 之所以要代理：Telegram 的文件地址形如 /file/bot<token>/<path>，Bot Token 就在
// URL 里，直接交给浏览器等于把凭据发出去。这条路由在 /api 下面，本身受登录会话
// 保护，头像字节由服务端取回后转发，Token 始终留在进程内。
func (h *BotHandler) groupAvatar(c *gin.Context) {
	groupID := strings.TrimSpace(c.Param("id"))
	if groupID == "" {
		c.Status(http.StatusNotFound)
		return
	}
	provider, ok := h.runtime.(groupAvatarRuntime)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	profileID := strings.TrimSpace(c.Query("bot_profile_id"))
	ctx, cancel := context.WithTimeout(c.Request.Context(), consoleGroupAvatarTimeout)
	defer cancel()
	avatar, found := provider.GroupAvatarForProfile(ctx, profileID, groupID)
	if !found {
		// 没有头像、机器人已退群、平台不支持——对前端都是一回事：显示占位图。
		c.Status(http.StatusNotFound)
		return
	}
	contentType := strings.TrimSpace(avatar.ContentType)
	// 只转发真正的图片，避免把平台返回的任意内容当图片喂给浏览器。
	if !strings.HasPrefix(contentType, "image/") {
		c.Status(http.StatusNotFound)
		return
	}
	// 群头像几乎不变，让浏览器自己缓存，别让列表每次刷新都回源。
	c.Header("Cache-Control", "private, max-age=3600")
	c.Data(http.StatusOK, contentType, avatar.Data)
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
	groups := mergeConsoleGroupItems(base, set, liveGroups, h.isOneBotProfile)
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
	if profileID != "" && !h.isOneBotProfile(profileID) {
		return h.localConsoleGroups(ctx, profileID)
	}
	live, liveAvailable, warning := h.liveConsoleGroups(ctx, refresh)
	if profileID != "" {
		return live, liveAvailable, warning
	}
	// 「全部机器人」以前只问 OneBot 的 get_group_list，于是非 OneBot 平台的群在
	// 默认视图里一个都不出现——纯 Telegram 部署打开这页就是空的。这里把本地
	// 事件里见过的群并进来，两边按群号去重。
	local, localAvailable, localWarning := h.localConsoleGroups(ctx, "")
	if !localAvailable {
		if !liveAvailable && warning == "" {
			warning = localWarning
		}
		return live, liveAvailable, warning
	}
	seen := make(map[string]struct{}, len(live))
	for _, item := range live {
		if id := strings.TrimSpace(item.GroupID); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, item := range local {
		id := strings.TrimSpace(item.GroupID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		live = append(live, item)
	}
	return live, true, warning
}

// localConsoleGroups 从本地事件历史里聚合群列表。Telegram、钉钉这类平台的 Bot
// API 没有「列出我加入的群」，机器人只有在群里收到过消息才知道自己在那儿。
// profileID 为空表示不限机器人。
func (h *BotHandler) localConsoleGroups(ctx context.Context, profileID string) ([]botAutoGroupInfo, bool, string) {
	if h.sqlite == nil {
		return nil, false, "当前存储不支持按机器人列出群，暂时只显示已保存的群配置"
	}
	seen, err := h.sqlite.ListInboundEventGroups(ctx, time.Now().Add(-consoleLocalGroupWindow), profileID)
	if err != nil || len(seen) == 0 {
		return nil, false, "这台机器人还没有在任何群里收到过消息，暂时只显示已保存的群配置"
	}
	groups := make([]botAutoGroupInfo, 0, len(seen))
	for _, item := range seen {
		groups = append(groups, botAutoGroupInfo{
			GroupID:      item.GroupID,
			GroupName:    h.resolveGroupName(ctx, item.BotProfileID, item.GroupID, item.GroupName),
			QQAvatar:     h.isOneBotProfile(item.BotProfileID),
			BotProfileID: item.BotProfileID,
		})
	}
	return groups, true, ""
}

// resolveGroupName 拿这个群此刻的名字。
//
// 事件 payload 里的名字只是「上次见到它时叫什么」：升级前收到的消息压根没记名字，
// 群改名之后也要等下一条消息才会更新。Telegram 的 getChat 只要有群号就能问到当前
// 名称，所以优先用它，问不到再退回事件里的旧名字。
//
// 结果按群缓存一段时间：群名很少变，而群管理页一次会列出几十个群，不缓存的话每次
// 打开页面都要挨个打一遍平台接口。
func (h *BotHandler) resolveGroupName(ctx context.Context, profileID, groupID, fallback string) string {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" || h.isOneBotProfile(profileID) {
		return fallback
	}
	provider, ok := h.runtime.(groupInfoRuntime)
	if !ok {
		return fallback
	}
	cacheKey := strings.TrimSpace(profileID) + "\x00" + groupID
	h.groupNameMu.Lock()
	cached, ok := h.groupNameCache[cacheKey]
	h.groupNameMu.Unlock()
	if ok && time.Since(cached.fetchedAt) < consoleGroupNameCacheTTL {
		if cached.name != "" {
			return cached.name
		}
		return fallback
	}
	callCtx, cancel := context.WithTimeout(ctx, consoleGroupNameTimeout)
	defer cancel()
	name := ""
	if info, found := provider.GroupInfoForProfile(callCtx, profileID, groupID); found {
		name = strings.TrimSpace(info.GroupName)
	}
	h.groupNameMu.Lock()
	if h.groupNameCache == nil {
		h.groupNameCache = map[string]groupNameCacheEntry{}
	}
	// 查不到也记一笔：机器人可能已经退群，或者这个平台不支持查询。不缓存空结果的话
	// 每次刷新都会为同一个群重试一遍。
	h.groupNameCache[cacheKey] = groupNameCacheEntry{name: name, fetchedAt: time.Now()}
	h.groupNameMu.Unlock()
	if name != "" {
		return name
	}
	return fallback
}

// isOneBotProfile 判断这台机器人是不是 OneBot 平台。
func (h *BotHandler) isOneBotProfile(profileID string) bool {
	if h.profiles == nil {
		return true
	}
	profiles := h.profiles.Profiles().Profiles
	profileID = strings.TrimSpace(profileID)
	for _, profile := range profiles {
		if strings.TrimSpace(profile.ID) == profileID {
			return assistant.IsOneBotPlatform(profile.Platform)
		}
	}
	// 认不出这个配置档时（单机器人部署的事件里 profile_id 往往是空的，老数据也
	// 可能对不上），不能一律当成 OneBot：那会给 Telegram 群拼出 QQ 的头像地址，
	// 图必然加载失败，看起来就是「没有头像」。只要当前没有任何 OneBot 机器人，
	// 就可以确定这个群不属于 OneBot。
	for _, profile := range profiles {
		if assistant.IsOneBotPlatform(profile.Platform) {
			return true
		}
	}
	return len(profiles) == 0
}

var (
	// consoleLocalGroupWindow 是「从本地事件推断在哪些群」的回看窗口。太短会漏掉
	// 冷群，太长会把早就退出的群一直挂在列表上。
	consoleLocalGroupWindow = 30 * 24 * time.Hour

	consoleLiveGroupTimeout  = 2500 * time.Millisecond
	consoleLiveGroupCacheTTL = 20 * time.Second

	// 群名很少变，缓存久一点；单个查询不值得让整页等太久。
	consoleGroupNameCacheTTL = 10 * time.Minute
	consoleGroupNameTimeout  = 1500 * time.Millisecond
	// 头像要多走一次文件下载，给的时间比查群名宽一些。
	consoleGroupAvatarTimeout = 5 * time.Second
)

type groupNameCacheEntry struct {
	name      string
	fetchedAt time.Time
}

// consoleGroupAvatarURL 拼出走本机代理的群头像地址。取不到头像时这条地址回 404，
// 前端按图片加载失败处理，显示占位图即可。
func consoleGroupAvatarURL(groupID, profileID string) string {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return ""
	}
	avatarURL := "/api/assistant/groups/" + url.PathEscape(groupID) + "/avatar"
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		avatarURL += "?bot_profile_id=" + url.QueryEscape(profileID)
	}
	return avatarURL
}

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
	// runtime 也要挡：这里要拿它去调 OneBot，少判一层的话没接机器人时直接空指针。
	// 群配置页一直有 runtime 所以没暴露过，事件筛选器接进来才踩到。
	if h == nil || h.runtime == nil {
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
	// 这一份来自 OneBot 的 get_group_list，群号就是 QQ 群号，头像规则适用。
	for index := range liveGroups {
		liveGroups[index].QQAvatar = true
	}
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

// qqAvatarForProfile 判断某台机器人的群能否套用 QQ 群头像地址规则。传函数而不是
// 整个 handler，是为了让 mergeConsoleGroupItems 保持成可单测的纯函数。
type qqAvatarForProfile func(profileID string) bool

func mergeConsoleGroupItems(base assistant.BotConfig, set assistant.GroupConfigSet, liveGroups []botAutoGroupInfo, qqAvatar qqAvatarForProfile) []consoleGroupItem {
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
		avatarURL := ""
		if live.QQAvatar {
			avatarURL = assistant.OneBotGroupAvatarURL(groupID)
		} else {
			avatarURL = consoleGroupAvatarURL(groupID, live.BotProfileID)
		}
		items = append(items, consoleGroupItem{
			GroupConfig:    cfg.WithDefaults(groupID, base),
			GroupName:      strings.TrimSpace(live.GroupName),
			AvatarURL:      avatarURL,
			MemberCount:    live.MemberCount,
			MaxMemberCount: live.MaxMemberCount,
			Configured:     configured,
			Joined:         true,
		})
		delete(saved, groupID)
	}
	for groupID, cfg := range saved {
		// 已保存的群配置自带归属机器人，据此判断能不能用 QQ 的头像规则。
		avatarURL := ""
		if qqAvatar == nil || qqAvatar(strings.TrimSpace(cfg.BotProfileID)) {
			avatarURL = assistant.OneBotGroupAvatarURL(groupID)
		} else {
			avatarURL = consoleGroupAvatarURL(groupID, cfg.BotProfileID)
		}
		items = append(items, consoleGroupItem{
			GroupConfig: cfg,
			AvatarURL:   avatarURL,
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
	profileID, profileName, err := h.consoleGroupProfile(payload.Config.BotProfileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	previous, _ := h.groupConfigs.ConfigForGroup(profileID, groupID)
	cfg, err := h.sanitizeGroupConfigPayload(payload.Config, groupID)
	// 群配置按机器人各存一份，保存时必须钉住是给哪一台配的。
	cfg.BotProfileID = profileID
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	saved, err := h.groupConfigs.SaveGroupConfig(cfg, h.runtime.Config())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.groups.save", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.groups.save", "群配置已保存（控制台）", groupID, groupConfigAuditMetadata(previous, saved, profileName))
	c.JSON(http.StatusOK, gin.H{"config": h.groupConfigForAPI(saved.WithDefaults(groupID, h.runtime.Config()))})
}

func (h *BotHandler) consoleGroupProfile(requested string) (string, string, error) {
	requested = strings.TrimSpace(requested)
	if h.profiles == nil {
		if requested == "" {
			return "", "", fmt.Errorf("群配置必须选择具体机器人")
		}
		return requested, requested, nil
	}
	set := h.profiles.Profiles().WithDefaults()
	if requested == "" && len(set.Profiles) == 1 {
		requested = strings.TrimSpace(set.Profiles[0].ID)
	}
	if requested == "" {
		return "", "", fmt.Errorf("群配置不能保存到“全部机器人”，请先选择具体机器人")
	}
	for _, profile := range set.Profiles {
		if strings.TrimSpace(profile.ID) == requested {
			return requested, assistant.NormalizeProfileName(profile.Name), nil
		}
	}
	return "", "", fmt.Errorf("机器人配置 %q 不存在", requested)
}

func groupConfigAuditMetadata(before, after assistant.GroupConfig, profileName string) map[string]any {
	metadata := map[string]any{
		"group_id": after.GroupID, "bot_profile_id": after.BotProfileID, "bot_profile_name": profileName,
	}
	gateSummary := func(gate *assistant.ReplyGate) map[string]any {
		if gate == nil {
			return map[string]any{"user_admission": "inherit", "allowed_users": 0, "blocked_users": 0}
		}
		return map[string]any{
			"user_admission": gate.UserAdmission,
			"allowed_users":  len(gate.AllowedUsers),
			"blocked_users":  len(gate.BlockedUsers),
		}
	}
	metadata["reply_gate_before"] = gateSummary(before.ReplyGate)
	metadata["reply_gate_after"] = gateSummary(after.ReplyGate)
	return metadata
}
