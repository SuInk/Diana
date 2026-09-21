// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type BotRuntime interface {
	Start(context.Context) error
	Stop() error
	// ApplyProfiles 换上整套机器人配置；channel 为 nil 表示只改行为配置、不重连。
	ApplyProfiles(context.Context, assistant.ProfileSet, assistant.Channel) error
	// ProfileConfig 取指定机器人的配置；ID 为空且只有一台时就是它。
	ProfileConfig(string) assistant.BotConfig
	ProfileConfigs() []assistant.BotConfig
	Status() assistant.RuntimeStatus
	CallOneBotAPI(context.Context, string, map[string]any) (map[string]any, error)
	CallOneBotAPIForProfile(context.Context, string, string, map[string]any) (map[string]any, error)
	SendGroupMessage(context.Context, string, string) (map[string]any, error)
	Plugins() *assistant.PluginManager
}

type BotChannelFactory func(assistant.BotConfig) assistant.Channel
type BotChannelSetFactory func(assistant.ProfileSet) assistant.Channel

// groupInfoRuntime 让群管理页按群号问平台要这个群此刻的信息。做成可选接口而不是
// 塞进 BotRuntime：只有 Telegram 这类「没有列出全部群的接口、但能按群号查」的平台
// 用得上，测试里的假运行时不必为此实现一个空方法。
type groupInfoRuntime interface {
	GroupInfoForProfile(ctx context.Context, profileID, groupID string) (assistant.GroupInfo, bool)
}

// groupAvatarRuntime 让群管理页能取到群头像的原始字节。头像地址在有些平台上带着
// 机器人凭据，只能由服务端取回后转发，因此这里传的是内容而不是 URL。
type groupAvatarRuntime interface {
	GroupAvatarForProfile(ctx context.Context, profileID, groupID string) (assistant.GroupAvatar, bool)
}

// contextBudgetRuntime 让事件页拿到按群算好的上下文预算分配。做成可选接口而不是
// 塞进 BotRuntime：它只服务一个页面，测试里的假运行时不必为此实现一个空方法。
type contextBudgetRuntime interface {
	ContextBudgetBreakdownForGroup(string) assistant.ContextBudgetBreakdown
}

// residentContextRuntime 让事件页拿到「每轮都注入」那几块的原文。和上面那条一样
// 做成可选接口：它只服务一个页面。
type residentContextRuntime interface {
	ResidentContextForGroup(ctx context.Context, profileID, groupID string) assistant.ResidentContextSnapshot
}

type repositoryWatchRuntime interface {
	CreateRepositoryWatch(context.Context, assistant.RepositoryWatchCreateInput) (assistant.Reminder, error)
	UpdateRepositoryWatch(context.Context, string, string, assistant.RepositoryWatchUpdateInput) (assistant.Reminder, error)
	CancelRepositoryWatch(string, string) (assistant.Reminder, error)
	DeleteRepositoryWatch(string, string) (bool, error)
	RunRepositoryWatchNow(string, string) (assistant.Reminder, error)
}

type historyBackfillRuntime interface {
	RequestHistoryBackfill(time.Duration) error
}

type rssWatchRuntime interface {
	CreateRSSWatch(context.Context, assistant.RSSWatchCreateInput) (assistant.Reminder, error)
	UpdateRSSWatch(context.Context, string, string, assistant.RSSWatchUpdateInput) (assistant.Reminder, error)
	CancelRSSWatch(string, string) (assistant.Reminder, error)
	DeleteRSSWatch(string, string) (bool, error)
}

type BotHandler struct {
	eventSummaryMu            sync.Mutex
	eventSummaryCache         map[string]eventSummaryCacheEntry
	runtime                   BotRuntime
	newChannel                BotChannelFactory
	newChannelSet             BotChannelSetFactory
	ctx                       context.Context
	profiles                  BotProfileStore
	groupConfigs              BotGroupConfigStore
	groupAdmin                *groupAdminVerifier
	localMedia                assistant.LocalMediaSharer
	sqlite                    *storage.SQLiteStore
	logs                      AppLogWriter
	features                  BotFeatureFlags
	installResolverDependency func(context.Context, string) (assistant.ResolverDependencyInstallResult, error)
	// repoPlugins / repoPluginSources 是第三方（仓库安装）插件的安装器与来源
	// 记录；未注入时相关接口返回 501，纯内置插件部署不受影响。
	repoPlugins       *assistant.RepoPluginInstaller
	repoPluginSources *assistant.RepoPluginStore
	liveGroupMu       sync.Mutex
	liveGroupCache    liveGroupListCache
	groupNameMu       sync.Mutex
	groupNameCache    map[string]groupNameCacheEntry
	userNameMu        sync.Mutex
	userNameCache     map[string]userNameCacheEntry
}

type BotFeatureFlags struct {
	GroupTest bool `json:"group_test"`
}

type pluginEnabledPayload struct {
	Enabled bool `json:"enabled"`
}

type pluginSettingsPayload struct {
	Inherit bool `json:"inherit,omitempty"`
	// Settings 是要保存的覆盖值全集，空 map 表示恢复默认。
	Settings map[string]any `json:"settings"`
	// ClearSecrets 列出要显式清除的凭据键。凭据不会因为没提交或提交空串
	// 而被清空，只有出现在这里才会真的删掉。
	ClearSecrets []string `json:"clear_secrets,omitempty"`
}

type musicConnectionTestPayload struct {
	Settings     map[string]any `json:"settings"`
	ClearSecrets []string       `json:"clear_secrets,omitempty"`
}

type groupTestPayload struct {
	GroupID string `json:"group_id"`
	Message string `json:"message"`
	OneShot bool   `json:"one_shot"`
}

type groupAdminChallengePayload struct {
	ProfileID string `json:"profile_id"`
	GroupID   string `json:"group_id"`
	UserID    string `json:"user_id"`
}

type groupAdminChallengeResponse struct {
	GroupID   string    `json:"group_id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Message   string    `json:"message"`
}

type groupAdminVerifyPayload struct {
	ProfileID string `json:"profile_id"`
	GroupID   string `json:"group_id"`
	UserID    string `json:"user_id"`
	Code      string `json:"code"`
}

type groupAdminSessionPayload struct {
	Token  string                `json:"token"`
	Config assistant.GroupConfig `json:"config,omitempty"`
}

type groupAdminConfigResponse struct {
	ProfileID string                  `json:"profile_id,omitempty"`
	GroupID   string                  `json:"group_id"`
	UserID    string                  `json:"user_id,omitempty"`
	Token     string                  `json:"token,omitempty"`
	ExpiresAt time.Time               `json:"expires_at,omitempty"`
	Config    assistant.GroupConfig   `json:"config"`
	Plugins   []assistant.PluginState `json:"plugins"`
	// Extensions 只给群管理员看「有哪些扩展、机器人给到哪一档」，不带工具清单。
	Extensions []groupAdminExtension `json:"extensions"`
}

type groupAdminExtension struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Bundled     bool   `json:"bundled,omitempty"`
	BotTier     string `json:"bot_tier"`
}

type groupTestResponse struct {
	GroupID      string                  `json:"group_id"`
	Message      string                  `json:"message,omitempty"`
	MessageID    string                  `json:"message_id,omitempty"`
	Sent         bool                    `json:"sent"`
	SendResult   map[string]any          `json:"send_result,omitempty"`
	Channel      assistant.ChannelStatus `json:"channel"`
	RecentEvents []assistant.EventRecord `json:"recent_events,omitempty"`
	Status       assistant.RuntimeStatus `json:"status"`
}

// 反向 WebSocket 的监听器只绑在本机，token 防的是同机上的其他进程冒连，不是公网
// 爆破。16 位挡掉了不少既有的、够用的 token（miku 线上那个就是 15 位），升级后只能
// 重新生成并同步改客户端。8 位是个能拦住手滑写个 "1234" 的下限。
const minBotTokenChars = 8

// NewBotHandler 创建 BotHandler 实例。
func NewBotHandler(ctx context.Context, runtime BotRuntime) *BotHandler {
	return NewBotHandlerWithFactory(ctx, runtime, func(cfg assistant.BotConfig) assistant.Channel {
		if channel := assistant.NewChannelForConfig(cfg); channel != nil {
			return channel
		}
		return assistant.NewOneBotReverseServer(assistant.OneBotConfig{
			Endpoint:    cfg.OneBotReverseWSEndpoint,
			AccessToken: cfg.OneBotAccessToken,
		})
	})
}

// NewBotHandlerWithFactory 创建 BotHandler 实例。
func NewBotHandlerWithFactory(ctx context.Context, runtime BotRuntime, factory BotChannelFactory) *BotHandler {
	handler := &BotHandler{
		runtime:                   runtime,
		newChannel:                factory,
		ctx:                       ctx,
		installResolverDependency: assistant.InstallResolverDependency,
		// 没有显式持久化 store 时，至少保证本次进程内也能按配置集语义工作。
		profiles:     NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: runtime.ProfileConfigs()}),
		groupConfigs: NewMemoryBotGroupConfigStore(),
		groupAdmin:   newGroupAdminVerifier(),
	}
	handler.linkGroupConfigProfiles()
	return handler
}

// linkGroupConfigProfiles 把机器人配置来源交给群配置存储。群配置默认跟随所属
// 机器人，存储归一化时得能自己查出「这个群是谁的」，否则只能拿当前这台顶上。
func (h *BotHandler) linkGroupConfigProfiles() {
	aware, ok := h.groupConfigs.(botGroupConfigProfileAware)
	if !ok || h.profiles == nil {
		return
	}
	aware.SetProfileSource(h.profiles)
}

// SetFeatureFlags 配置只应在显式测试环境开放的 WebUI 功能。
func (h *BotHandler) SetFeatureFlags(flags BotFeatureFlags) {
	h.features = flags
}

// SetLocalMediaSharer lets OneBot fetch local diagnostic files over HTTP.
func (h *BotHandler) SetLocalMediaSharer(sharer assistant.LocalMediaSharer) {
	h.localMedia = sharer
}

// SetProfileStore 注入 OneBot v11 机器人配置集存储。
func (h *BotHandler) SetProfileStore(store BotProfileStore) {
	if store == nil {
		return
	}
	h.profiles = store
	h.linkGroupConfigProfiles()
}

// SetChannelSetFactory enables all configured transports to be rebuilt as one
// routed channel whenever a profile is saved, enabled, disabled, or deleted.
func (h *BotHandler) SetChannelSetFactory(factory BotChannelSetFactory) {
	h.newChannelSet = factory
}

// SetGroupConfigStore 注入 群级配置存储。
func (h *BotHandler) SetGroupConfigStore(store BotGroupConfigStore) {
	if store == nil {
		return
	}
	h.groupConfigs = store
	h.linkGroupConfigProfiles()
}

// SetSQLiteStore 注入 SQLite，用于插件状态持久化和操作日志。
func (h *BotHandler) SetSQLiteStore(store *storage.SQLiteStore) {
	h.sqlite = store
	h.logs = store
}

// Register registers the assistant API.
func (h *BotHandler) Register(router gin.IRouter) {
	h.registerRoutes(router, "/api/assistant")
	// 控制台登录用户直接管理全部群配置，无需群验证码流程。
	h.registerConsoleGroupRoutes(router)
}

func (h *BotHandler) registerRoutes(router gin.IRouter, base string) {
	router.GET(base+"/config", h.getConfig)
	router.GET(base+"/config/defaults", h.newProfileDefaults)
	router.POST(base+"/config/new", h.createProfile)
	router.GET(base+"/platforms", h.platforms)
	router.POST(base+"/config", h.saveConfig)
	router.POST(base+"/config/clone", h.cloneProfile)
	router.POST(base+"/config/delete", h.deleteProfile)
	router.POST(base+"/config/message-relays", h.setMessageRelays)
	router.POST(base+"/config/profile-enabled", h.setProfileEnabled)
	router.POST(base+"/config/profiles-enabled", h.setAllProfilesEnabled)
	router.GET(base+"/agent-defaults", h.agentDefaults)
	router.GET(base+"/features", h.featuresStatus)
	router.GET(base+"/status", h.status)
	router.GET(base+"/auto-info", h.autoInfo)
	router.GET(base+"/dashboard-stats", h.dashboardStats)
	router.GET(base+"/events", h.listEvents)
	router.GET(base+"/events/:id/trace", h.eventTrace)
	router.GET(base+"/events/:id/images/:index", h.eventImage)
	router.GET(base+"/events/:id/outbound-images/:index", h.eventOutboundImage)
	router.GET(base+"/stickers", h.listStickers)
	router.GET(base+"/stickers/:hash/image", h.stickerImage)
	router.GET(base+"/users", h.listAssistantUsers)
	router.GET(base+"/user-names", h.lookupAssistantUserNames)
	router.GET(base+"/users/:id", h.getAssistantUser)
	router.PUT(base+"/users/:id", h.editAssistantUser)
	router.DELETE(base+"/users/:id", h.editAssistantUser)
	router.DELETE(base+"/users/:id/memories", h.clearAssistantUserMemories)
	router.DELETE(base+"/users/:id/memories/:memory", h.clearAssistantUserMemories)
	h.registerPersonaRoutes(router, base)
	h.registerCharacterCardRoutes(router, base)
	h.registerWorldBookRoutes(router, base)
	router.GET(base+"/notebook", h.listNotebook)
	router.GET(base+"/notebook/entry", h.getNotebookEntry)
	router.POST(base+"/notebook", h.saveNotebookEntry)
	router.POST(base+"/notebook/delete", h.deleteNotebookEntry)
	router.POST(base+"/notebook/restore", h.restoreNotebookEntry)
	router.GET(base+"/tasks", h.listTasks)
	router.POST(base+"/tasks/repository-watches", h.createRepositoryWatch)
	router.PUT(base+"/tasks/repository-watches/:id", h.updateRepositoryWatch)
	router.POST(base+"/tasks/repository-watches/:id/cancel", h.cancelRepositoryWatch)
	router.POST(base+"/tasks/repository-watches/:id/run", h.runRepositoryWatch)
	router.DELETE(base+"/tasks/repository-watches/:id", h.deleteRepositoryWatch)
	router.POST(base+"/tasks/rss-watches", h.createRSSWatch)
	router.PUT(base+"/tasks/rss-watches/:id", h.updateRSSWatch)
	router.POST(base+"/tasks/rss-watches/:id/cancel", h.cancelRSSWatch)
	router.DELETE(base+"/tasks/rss-watches/:id", h.deleteRSSWatch)
	router.POST(base+"/start", h.start)
	router.POST(base+"/stop", h.stop)
	router.POST(base+"/backfill", h.requestBackfill)
	if h.features.GroupTest {
		router.GET(base+"/group-test", h.getGroupTest)
		router.GET(base+"/group-test/files", h.listGroupTestFiles)
		router.POST(base+"/group-test", h.sendGroupTest)
		router.POST(base+"/group-test/recall", h.recallGroupTestMessage)
		router.POST(base+"/group-test/file", h.parseGroupTestFile)
		router.POST(base+"/group-test/napcat-qrcode", h.shareNapCatQRCode)
		router.POST(base+"/group-test/upload-file", h.uploadGroupTestFile)
		router.POST(base+"/group-test/onebot", h.callGroupTestOneBot)
	}
	router.GET(base+"/plugins", h.listPlugins)
	router.GET(base+"/extensions", h.extensions)
	router.POST(base+"/extensions", h.extensions)
	router.GET(base+"/plugins/dependencies", h.pluginDependencies)
	router.POST(base+"/plugins/dependencies/:name/install", h.installPluginDependency)
	router.POST(base+"/plugins/:id/install", h.installPlugin)
	router.POST(base+"/plugins/:id/uninstall", h.uninstallPlugin)
	router.POST(base+"/plugins/:id/enabled", h.setPluginEnabled)
	router.POST(base+"/plugins/:id/settings", h.updatePluginSettings)
	// 第三方（仓库安装）插件。repo/* 是静态段，gin 里与 :id 参数段共存不冲突。
	router.POST(base+"/plugins/repo/preview", h.previewRepoPlugin)
	router.POST(base+"/plugins/repo/install", h.installRepoPlugin)
	router.POST(base+"/plugins/repo/update/:id", h.updateRepoPlugin)
	router.POST(base+"/plugins/music/test", h.testMusicConnections)
	router.POST(base+"/plugins/coding-agent/setup", h.codingAgentSetup)
	router.POST(base+"/plugins/repository-publish/issues", h.createRepositoryIssue)
	router.GET(base+"/plugins/repository-publish/drafts", h.listRepositoryIssueDrafts)
	router.POST(base+"/plugins/repository-publish/drafts/:id/publish", h.publishRepositoryIssueDraft)
	router.POST(base+"/plugins/repository-publish/drafts/:id/restore", h.restoreRepositoryIssueDraft)
	router.PATCH(base+"/plugins/repository-publish/drafts/:id", h.editRepositoryIssueDraft)
	router.DELETE(base+"/plugins/repository-publish/drafts/:id", h.deleteRepositoryIssueDraft)
	router.POST(base+"/group-admin/challenge", h.startGroupAdminChallenge)
	router.POST(base+"/group-admin/verify", h.verifyGroupAdminChallenge)
	router.GET(base+"/group-admin/config", h.getGroupAdminConfig)
	router.POST(base+"/group-admin/config", h.saveGroupAdminConfig)
}

// platforms 返回可用于创建机器人配置的平台及协议适配器。
func (h *BotHandler) platforms(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"platforms": assistant.SupportedPlatforms()})
}

// getConfig 处理 OneBot v11 机器人配置读取请求。
// 默认响应把 Access Token 一类凭据抹成占位符;配置页要查看真实值时显式带
// include_secrets=true 再要一次,和 LLM API Key 那套保持一致。
func (h *BotHandler) getConfig(c *gin.Context) {
	if queryBool(c.Query("include_secrets")) {
		c.JSON(http.StatusOK, assistant.PayloadFromProfileSetWithSecrets(h.profiles.Profiles(), botProfileScope(c)))
		return
	}
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(h.profiles.Profiles(), botProfileScope(c)))
}

// newProfileDefaults returns a fresh draft, never an existing profile or its secrets.
func (h *BotHandler) newProfileDefaults(c *gin.Context) {
	platform := assistant.NormalizePlatformID(c.Query("platform"))
	if err := assistant.ValidatePlatform(platform); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defaults := assistant.DefaultBotConfig()
	defaults.Platform = platform
	defaults.Enabled = true
	defaults.OwnerLoginEnabled = true
	c.JSON(http.StatusOK, assistant.PayloadFromConfig(defaults))
}

func (h *BotHandler) createProfile(c *gin.Context) {
	h.saveProfile(c, true)
}

// agentDefaults 返回新建机器人时用的 Agent 推荐默认值。
//
// 存在的理由是「装完就能用」这件事对存量部署不成立：默认值只作用于新建配置，
// 已经在跑的部署升级后不会凭空多出命令执行和文件写入（那是静默扩权）。于是
// 老部署想要这些能力，就得知道该往白名单里填什么——这个接口把那份清单给出来，
// 控制台据此提供一次「填入推荐默认值」，填完仍需用户自己点保存。
//
// 只读，不改任何东西：真正的授权动作发生在用户点保存的那一刻。
func (h *BotHandler) agentDefaults(c *gin.Context) {
	defaults := assistant.DefaultBotConfig()
	c.JSON(http.StatusOK, gin.H{
		"agent_command_allowlist":  defaults.AgentCommandAllowlist,
		"agent_file_write_enabled": defaults.AgentFileWriteEnabled,
		"agent_command_sandbox":    defaults.AgentCommandSandbox,
		"agent_max_steps":          defaults.AgentMaxSteps,
		"agent_command_timeout_ms": defaults.AgentCommandTimeoutMS,
	})
}

// featuresStatus 返回当前 WebUI 暴露的 OneBot v11 机器人测试能力。
func (h *BotHandler) featuresStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.features)
}

// saveConfig 保存当前机器人配置或新增机器人配置档。
func (h *BotHandler) saveConfig(c *gin.Context) {
	h.saveProfile(c, false)
}

func (h *BotHandler) saveProfile(c *gin.Context, create bool) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "config_save", err, "", nil)
		return
	}

	set := h.profiles.Profiles()
	existing := existingBotProfileConfig(set, payload)
	if create {
		// Creation cannot inherit credentials or overwrite an existing profile ID.
		existing = assistant.DefaultBotConfig()
		payload.ID = ""
		payload.Profiles = nil
	}
	cfg := assistant.ConfigFromPayload(payload, existing)
	// Legacy edit requests omit the ID; keep their current profile identity so
	// the duplicate check does not mistake an edit for a second connection.
	if !create && cfg.ID == "" {
		cfg.ID = existing.ID
	}
	if err := set.ValidateIndependentConnection(cfg); err != nil {
		h.writeError(c, http.StatusBadRequest, "config_save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := validateTokenLength("onebot_access_token", payload.OneBotAccessToken); err != nil {
		h.writeError(c, http.StatusBadRequest, "config_save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := validateTokenLength("nonebot_bridge_token", payload.NoneBotBridgeToken); err != nil {
		h.writeError(c, http.StatusBadRequest, "config_save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := cfg.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "config_save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}

	next, savedID := upsertBotProfileSet(set, payload, cfg)
	current, ok := next.ConfigForProfile(savedID)
	if !ok {
		h.writeError(c, http.StatusBadRequest, "config_save", fmt.Errorf("diana profile set is empty"), "", nil)
		return
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "config_save", err, botLogTarget(current), botLogMetadata(current))
		return
	}
	// 落库失败就不能回 200：以前这里吞掉错误，前端提示保存成功，重启后配置
	// 又是旧的，只能靠翻数据库才发现。
	if err := h.profiles.SaveProfiles(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "config_save", err, botLogTarget(current), botLogMetadata(current))
		return
	}
	recordRequestOperation(c, h.logs, "config_save", "OneBot v11 机器人配置已保存", current.ID, botLogMetadata(current))
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next, savedID))
}

// cloneProfile 复制指定 OneBot v11 机器人配置档。
func (h *BotHandler) cloneProfile(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "profile_clone", err, "", nil)
		return
	}
	sourceID := strings.TrimSpace(payload.ID)
	set := h.profiles.Profiles()
	if sourceID == "" {
		h.writeError(c, http.StatusBadRequest, "profile_clone", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	for _, profile := range set.Profiles {
		if profile.ID != sourceID {
			continue
		}
		cloned := profile
		cloned.ID = ""
		cloned.Name = profile.Name + " 副本"
		// A cloned credential must never start a second poller/socket until the
		// administrator explicitly enables it.
		cloned.Enabled = false
		next, clonedID := upsertBotProfileSet(set, assistant.ConfigPayload{Name: cloned.Name}, cloned)
		current, ok := next.ConfigForProfile(clonedID)
		if !ok {
			h.writeError(c, http.StatusBadRequest, "profile_clone", fmt.Errorf("diana profile set is empty"), "", nil)
			return
		}
		if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
			h.writeError(c, http.StatusBadRequest, "profile_clone", err, botLogTarget(current), botLogMetadata(current))
			return
		}
		if err := h.profiles.SaveProfiles(next); err != nil {
			h.writeError(c, http.StatusInternalServerError, "profile_clone", err, botLogTarget(current), botLogMetadata(current))
			return
		}
		recordRequestOperation(c, h.logs, "profile_clone", "OneBot v11 机器人配置已复制", sourceID, botLogMetadata(profile))
		c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next, clonedID))
		return
	}
	h.writeError(c, http.StatusNotFound, "profile_clone", fmt.Errorf("profile %q not found", sourceID), sourceID, nil)
}

// deleteProfile 删除指定 OneBot v11 机器人配置档。
func (h *BotHandler) deleteProfile(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "profile_delete", err, "", nil)
		return
	}
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		h.writeError(c, http.StatusBadRequest, "profile_delete", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	set := h.profiles.Profiles()
	if len(set.Profiles) <= 1 {
		h.writeError(c, http.StatusBadRequest, "profile_delete", fmt.Errorf("at least one qqbot profile must remain"), targetID, nil)
		return
	}
	next := set.Delete(targetID)
	if len(next.Profiles) == len(set.Profiles) {
		h.writeError(c, http.StatusNotFound, "profile_delete", fmt.Errorf("profile %q not found", targetID), targetID, nil)
		return
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "profile_delete", err, targetID, map[string]any{"profile_id": targetID})
		return
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "profile_delete", err, targetID, map[string]any{"profile_id": targetID})
		return
	}
	recordRequestOperation(c, h.logs, "profile_delete", "OneBot v11 机器人配置已删除", targetID, map[string]any{"profile_id": targetID})
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next, ""))
}

type messageRelayPayload struct {
	Relays []assistant.MessageRelayPair `json:"relays"`
}

// setMessageRelays 整体替换消息互通链路。
//
// 整体替换而不是逐条增删：链路是两端一对的小对象，前端本来就是拿着完整列表在
// 编辑，一次提交也省掉了并发改动时半新半旧的中间状态。
func (h *BotHandler) setMessageRelays(c *gin.Context) {
	var payload messageRelayPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "message_relays_update", err, "", nil)
		return
	}
	next := h.profiles.Profiles().WithMessageRelays(payload.Relays)
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "message_relays_update", err, "", nil)
		return
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "message_relays_update", err, "", map[string]any{"relays": len(next.MessageRelays)})
		return
	}
	recordRequestOperation(c, h.logs, "message_relays_update", "消息互通链路已更新", "", map[string]any{"relays": len(next.MessageRelays)})
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next, botProfileScope(c)))
}

type profileEnabledPayload struct {
	ProfileID string `json:"profile_id"`
	Enabled   bool   `json:"enabled"`
}

// startRuntimeAfterEnable 在「把机器人设成启用」之后把停着的运行时拉起来。
//
// ApplyProfiles 只会重启本来就在跑的运行时，停着的它一概不碰（runtime.ApplyProfiles
// 里 !wasRunning 直接返回）。于是「运行时停着的时候启用一台机器人」这个操作以前
// 会返回 200、界面把开关点亮，实际什么都没启动，也没有任何提示——接入端反连过来
// 一律被 503 挡掉，控制台却只显示「等待连接」。启用本身就是「我要它跑起来」，
// 这里顺手补上那一次 Start。
//
// 只在启用路径上做，配置保存这类路径不碰：用户明确按过「停止」之后再去改配置，
// 不该被一次保存悄悄复活。起不来时不让整个请求失败——配置已经存好了，启动失败的
// 原因留在运行时状态里（Start 会写进 lastError），前端照常能读到。
func (h *BotHandler) startRuntimeAfterEnable() {
	if h.runtime == nil || h.runtime.Status().Running {
		return
	}
	if err := h.runtime.Start(h.ctx); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		log.Printf("enable requested but runtime start failed: %v", err)
	}
}

// setProfileEnabled 只切换单台机器人的启用状态，其他机器人不受影响；
// 启停某一台不需要重配其余档案。
func (h *BotHandler) setProfileEnabled(c *gin.Context) {
	var payload profileEnabledPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "profile_enabled", err, "", nil)
		return
	}
	next, ok := h.profiles.Profiles().WithProfileEnabled(payload.ProfileID, payload.Enabled)
	if !ok {
		h.writeError(c, http.StatusNotFound, "profile_enabled", fmt.Errorf("profile %q not found", payload.ProfileID), payload.ProfileID, nil)
		return
	}
	current, _ := next.ConfigForProfile(payload.ProfileID)
	if payload.Enabled {
		if err := next.ValidateIndependentConnection(current); err != nil {
			h.writeError(c, http.StatusBadRequest, "profile_enabled", err, botLogTarget(current), botLogMetadata(current))
			return
		}
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "profile_enabled", err, botLogTarget(current), botLogMetadata(current))
		return
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "profile_enabled", err, botLogTarget(current), map[string]any{"enabled": payload.Enabled})
		return
	}
	status := "机器人已停用"
	if payload.Enabled {
		status = "机器人已启用"
		h.startRuntimeAfterEnable()
	}
	recordRequestOperation(c, h.logs, "profile_enabled", status, current.ID, botLogMetadata(current))
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next, current.ID))
}

// setAllProfilesEnabled 统一启用或停用全部机器人；卡片上的批量开关走这里，
// 下方每台机器人的状态随配置集一起更新。
func (h *BotHandler) setAllProfilesEnabled(c *gin.Context) {
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "profiles_enabled", err, "", nil)
		return
	}
	next := h.profiles.Profiles().WithAllProfilesEnabled(payload.Enabled)
	if payload.Enabled {
		for _, profile := range next.Profiles {
			if err := next.ValidateIndependentConnection(profile); err != nil {
				h.writeError(c, http.StatusBadRequest, "profiles_enabled", err, botLogTarget(profile), botLogMetadata(profile))
				return
			}
		}
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "profiles_enabled", err, "", nil)
		return
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "profiles_enabled", err, "", map[string]any{"enabled": payload.Enabled})
		return
	}
	status := "全部机器人已停用"
	if payload.Enabled {
		status = "全部机器人已启用"
		h.startRuntimeAfterEnable()
	}
	recordRequestOperation(c, h.logs, "profiles_enabled", status, "", map[string]any{"enabled": payload.Enabled})
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next, botProfileScope(c)))
}

func (h *BotHandler) applyProfileSet(set assistant.ProfileSet) error {
	set = set.WithDefaults()
	if err := set.ValidateConnections(); err != nil {
		return err
	}
	if len(set.Profiles) == 0 {
		return fmt.Errorf("assistant profile set is empty")
	}
	previous := h.profiles.Profiles().WithDefaults()
	if !profileSetRequiresReconnect(previous, set) {
		return h.runtime.ApplyProfiles(h.ctx, set, nil)
	}
	return h.runtime.ApplyProfiles(h.ctx, set, h.newChannelForSet(set))
}

// newChannelForSet 为整套机器人配置建连接。只在没有配置集工厂时退回单配置工厂：
// 以前两个都调，单配置工厂造出来的 channel 直接被丢弃，但它有副作用——OneBot 反连
// 监听器是进程内共享的一个实例，那次调用会用某一台的 token 覆盖监听器，握手一律 401。
func (h *BotHandler) newChannelForSet(set assistant.ProfileSet) assistant.Channel {
	if h.newChannelSet != nil {
		return h.newChannelSet(set)
	}
	if h.newChannel == nil || len(set.Profiles) == 0 {
		return nil
	}
	return h.newChannel(set.Profiles[0])
}

type botTransportConfig struct {
	ConnectionProfileID string
	ID                  string
	Platform            string
	OneBotTransport     string
	OneBotWSEndpoint    string
	OneBotHTTPURL       string
	OneBotHTTPSecret    string
	OneBotEndpoint      string
	OneBotAccessToken   string
	TelegramBotToken    string
	TelegramAPIBaseURL  string
	TelegramProxyURL    string
}

func profileSetRequiresReconnect(previous, next assistant.ProfileSet) bool {
	previous = previous.WithDefaults()
	next = next.WithDefaults()
	// 处理流水线的并发上限取各台启用机器人里最大的，变了就要重建。
	if maxEnabledConcurrency(previous) != maxEnabledConcurrency(next) {
		return true
	}
	return !reflect.DeepEqual(enabledBotTransports(previous), enabledBotTransports(next))
}

func maxEnabledConcurrency(set assistant.ProfileSet) int {
	limit := 0
	for _, profile := range set.WithDefaults().Profiles {
		if profile.Enabled {
			limit = max(limit, profile.MaxBotConcurrency)
		}
	}
	return limit
}

func enabledBotTransports(set assistant.ProfileSet) []botTransportConfig {
	transports := make([]botTransportConfig, 0, len(set.Profiles))
	for _, profile := range set.WithDefaults().Profiles {
		profile = profile.WithDefaults()
		if !profile.Enabled {
			continue
		}
		profile, _ = set.ResolveConnection(profile)
		transports = append(transports, botTransportConfig{
			ConnectionProfileID: profile.ConnectionProfileID,
			ID:                  profile.ID,
			Platform:            profile.Platform,
			OneBotTransport:     profile.OneBotTransport,
			OneBotWSEndpoint:    profile.OneBotWSEndpoint,
			OneBotHTTPURL:       profile.OneBotHTTPURL,
			OneBotHTTPSecret:    profile.OneBotHTTPSecret,
			OneBotEndpoint:      profile.OneBotReverseWSEndpoint,
			OneBotAccessToken:   profile.OneBotAccessToken,
			TelegramBotToken:    profile.TelegramBotToken,
			TelegramAPIBaseURL:  profile.TelegramAPIBaseURL,
			TelegramProxyURL:    profile.TelegramProxyURL,
		})
	}
	return transports
}

// validateTokenLength 校验用户显式填写的 token 长度。
func validateTokenLength(field string, value string) error {
	// 空 token 表示不鉴权或沿用旧值；只有用户显式填写时才检查强度。
	if value == "" {
		return nil
	}
	if utf8.RuneCountInString(value) < minBotTokenChars {
		return fmt.Errorf("%s must be at least %d characters", field, minBotTokenChars)
	}
	return nil
}

// status 返回 OneBot v11 机器人运行状态快照。
func (h *BotHandler) status(c *gin.Context) {
	c.JSON(http.StatusOK, h.runtime.Status())
}

// start 处理启动 OneBot v11 机器人的请求。
func (h *BotHandler) start(c *gin.Context) {
	if err := h.runtime.Start(h.ctx); err != nil {
		h.writeError(c, http.StatusBadRequest, "start", err, "", h.runtimeLogMetadata())
		return
	}
	recordRequestOperation(c, h.logs, "start", "机器人运行时已启动", "", h.runtimeLogMetadata())
	c.JSON(http.StatusOK, h.runtime.Status())
}

// stop 处理停止 OneBot v11 机器人的请求。
func (h *BotHandler) stop(c *gin.Context) {
	if err := h.runtime.Stop(); err != nil {
		h.writeError(c, http.StatusBadRequest, "stop", err, "", h.runtimeLogMetadata())
		return
	}
	recordRequestOperation(c, h.logs, "stop", "机器人运行时已停止", "", h.runtimeLogMetadata())
	c.JSON(http.StatusOK, h.runtime.Status())
}

// requestBackfill 手动触发一次历史消息回补，用于实测回补效果。
func (h *BotHandler) requestBackfill(c *gin.Context) {
	runtime, ok := h.runtime.(historyBackfillRuntime)
	if !ok {
		h.writeError(c, http.StatusNotImplemented, "backfill", fmt.Errorf("runtime does not support manual history backfill"), "", h.runtimeLogMetadata())
		return
	}
	var payload struct {
		Hours float64 `json:"hours"`
	}
	// body 可省略，默认回补允许的最大窗口。
	_ = c.ShouldBindJSON(&payload)
	window := time.Duration(payload.Hours * float64(time.Hour))
	if window <= 0 || window > assistant.InboundReplayWindow {
		window = assistant.InboundReplayWindow
	}
	if err := runtime.RequestHistoryBackfill(window); err != nil {
		h.writeError(c, http.StatusConflict, "backfill", err, "", h.runtimeLogMetadata())
		return
	}
	recordRequestOperation(c, h.logs, "backfill", fmt.Sprintf("已触发手动回补，窗口 %s", window), "", h.runtimeLogMetadata())
	c.JSON(http.StatusOK, gin.H{"requested": true, "window_hours": window.Hours()})
}

// getGroupTest 返回指定群最近收发事件，辅助真实 群联调。
func (h *BotHandler) getGroupTest(c *gin.Context) {
	groupID := strings.TrimSpace(c.Query("group_id"))
	if groupID == "" {
		h.writeError(c, http.StatusBadRequest, "group_test_status", fmt.Errorf("group_id is required"), "", nil)
		return
	}
	status := h.runtime.Status()
	c.JSON(http.StatusOK, groupTestResponse{
		GroupID:      groupID,
		Channel:      status.Channel,
		RecentEvents: groupEvents(status.RecentEvents, groupID),
		Status:       status,
	})
}

// sendGroupTest 通过当前 OneBot 连接向 群发送测试消息，并返回近期收到的同群事件。
func (h *BotHandler) sendGroupTest(c *gin.Context) {
	var payload groupTestPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "group_test_send", err, "", nil)
		return
	}
	groupID := strings.TrimSpace(payload.GroupID)
	message := strings.TrimSpace(payload.Message)
	if groupID == "" {
		h.writeError(c, http.StatusBadRequest, "group_test_send", fmt.Errorf("group_id is required"), "", nil)
		return
	}
	if message == "" {
		h.writeError(c, http.StatusBadRequest, "group_test_send", fmt.Errorf("message is required"), groupID, map[string]any{"group_id": groupID})
		return
	}
	var sendResult map[string]any
	var err error
	if payload.OneShot {
		groupNumber, parseErr := strconv.ParseInt(groupID, 10, 64)
		if parseErr != nil || groupNumber <= 0 {
			h.writeError(c, http.StatusBadRequest, "group_test_send", fmt.Errorf("valid group_id is required"), groupID, nil)
			return
		}
		segments := assistant.TextToOneBotSegments(message)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		sendResult, err = h.runtime.CallOneBotAPI(ctx, "send_group_msg", map[string]any{"group_id": groupNumber, "message": segments})
	} else {
		sendResult, err = h.runtime.SendGroupMessage(c.Request.Context(), groupID, message)
	}
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "group_test_send", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	messageID := oneBotMessageID(sendResult)
	status := h.runtime.Status()
	recordRequestOperation(c, h.logs, "group_test_send", "群测试消息已发送", groupID, map[string]any{
		"group_id":   groupID,
		"message_id": messageID,
	})
	c.JSON(http.StatusOK, groupTestResponse{
		GroupID:      groupID,
		Message:      message,
		MessageID:    messageID,
		Sent:         true,
		SendResult:   sendResult,
		Channel:      status.Channel,
		RecentEvents: groupEvents(status.RecentEvents, groupID),
		Status:       status,
	})
}

// withRepoSource 给第三方仓库插件的响应附上安装来源，插件页据此展示更新入口。
func (h *BotHandler) withRepoSource(state assistant.PluginState) assistant.PluginState {
	if h.repoPluginSources == nil {
		return state
	}
	if source, ok := h.repoPluginSources.Get(state.Manifest.ID); ok {
		source := source
		state.RepoSource = &source
	}
	return state
}

// listPlugins 返回机器人插件列表。
func (h *BotHandler) listPlugins(c *gin.Context) {
	profileID, ok := h.pluginProfileScope(c)
	if !ok {
		return
	}
	states := h.runtime.Plugins().ListVisibleForProfile(profileID)
	visible := make([]assistant.PluginState, 0, len(states))
	for _, state := range states {
		if profileID == "" || state.Manifest.ID != assistant.OpenAPIPluginID {
			visible = append(visible, h.withRepoSource(state))
		}
	}
	c.JSON(http.StatusOK, assistant.RedactStates(visible))
}

func (h *BotHandler) pluginProfileScope(c *gin.Context) (string, bool) {
	profileID := strings.TrimSpace(c.Query("profile"))
	if profileID == "" {
		if c.Param("id") == "" {
			return "", true
		}
		if !strings.HasSuffix(c.Request.URL.Path, "/enabled") {
			return "", true
		}
		if c.Param("id") == assistant.OpenAPIPluginID {
			return "", true
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要切换插件启用状态的机器人"})
		return "", false
	}
	if c.Param("id") == assistant.OpenAPIPluginID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "OpenAPI 请在系统设置中配置"})
		return "", false
	}
	for _, profile := range h.profiles.Profiles().Profiles {
		if profile.ID == profileID {
			return profileID, true
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "robot profile not found"})
	return "", false
}

// pluginDependencies 返回各插件外部依赖的探测结果，让控制台能直接看出
// yt-dlp / ffmpeg / node / 浏览器是否齐全，而不是等用户发链接后才报错。
//
// plugins 按插件 ID 分组，界面据此决定在哪张卡片上显示。
func (h *BotHandler) pluginDependencies(c *gin.Context) {
	resolver := assistant.ResolverDependencies()
	browser := assistant.BrowserDependencies()
	if queryBool(c.Query("refresh")) {
		resolver = assistant.RefreshResolverDependencies()
		browser = assistant.RefreshBrowserDependencies()
	}
	c.JSON(http.StatusOK, gin.H{
		"plugins": gin.H{
			assistant.ResolverPluginID:         resolver,
			assistant.SandboxedBrowserPluginID: browser,
			assistant.GroupRelationsPluginID:   assistant.RelationRenderDependencies(browser),
		},
	})
}

// installPluginDependency 安装链接解析插件白名单中的外部命令。
func (h *BotHandler) installPluginDependency(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	result, err := h.installResolverDependency(c.Request.Context(), name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, assistant.ErrUnknownResolverDependency):
			status = http.StatusNotFound
		case errors.Is(err, assistant.ErrResolverInstallerUnavailable):
			status = http.StatusNotImplemented
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		h.writeError(c, status, "plugin_dependency_install", err, name, map[string]any{"dependency": name})
		return
	}
	recordRequestOperation(c, h.logs, "plugin_dependency_install", "插件运行依赖已安装", name, map[string]any{
		"dependency": name,
		"installer":  result.Installer,
		"version":    result.Dependency.Version,
	})
	c.JSON(http.StatusOK, result)
}

// installPlugin 处理插件安装请求。
func (h *BotHandler) installPlugin(c *gin.Context) {
	state, err := h.runtime.Plugins().Install(c.Param("id"))
	if err != nil {
		h.writePluginError(c, "plugin_install", err, c.Param("id"))
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "plugin_install", "机器人插件已安装", state.Manifest.ID, pluginLogMetadata(state))
	c.JSON(http.StatusOK, state.Redacted())
}

// uninstallPlugin 处理插件卸载请求。
func (h *BotHandler) uninstallPlugin(c *gin.Context) {
	state, err := h.runtime.Plugins().Uninstall(c.Param("id"))
	if err != nil {
		h.writePluginError(c, "plugin_uninstall", err, c.Param("id"))
		return
	}
	// 第三方插件连落盘目录和来源记录一起清掉；非仓库插件这里静默返回。
	h.removeRepoPluginSources(c.Param("id"))
	h.persistState()
	h.removeRepoPluginSources(state.Manifest.ID)
	recordRequestOperation(c, h.logs, "plugin_uninstall", "机器人插件已卸载", state.Manifest.ID, pluginLogMetadata(state))
	c.JSON(http.StatusOK, state.Redacted())
}

// setPluginEnabled 处理插件启用状态变更请求。
func (h *BotHandler) setPluginEnabled(c *gin.Context) {
	profileID, ok := h.pluginProfileScope(c)
	if !ok {
		return
	}
	var payload pluginEnabledPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "plugin_enabled", err, c.Param("id"), map[string]any{"plugin_id": c.Param("id")})
		return
	}
	state, err := h.runtime.Plugins().SetEnabledForProfile(c.Param("id"), profileID, payload.Enabled)
	if err != nil {
		h.writePluginError(c, "plugin_enabled", err, c.Param("id"))
		return
	}
	h.persistState()
	metadata := pluginLogMetadata(state)
	metadata["profile_id"] = profileID
	recordRequestOperation(c, h.logs, "plugin_enabled", "机器人插件开关已更新", state.Manifest.ID, metadata)
	c.JSON(http.StatusOK, state.Redacted())
}

// updatePluginSettings 处理插件详细设置变更请求。
func (h *BotHandler) updatePluginSettings(c *gin.Context) {
	profileID, ok := h.pluginProfileScope(c)
	if !ok {
		return
	}
	var payload pluginSettingsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "plugin_settings", err, c.Param("id"), map[string]any{"plugin_id": c.Param("id")})
		return
	}
	var state assistant.PluginState
	var err error
	if payload.Inherit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "插件配置不再支持继承"})
		return
	} else {
		state, err = h.runtime.Plugins().UpdateSettingsForProfile(c.Param("id"), profileID, payload.Settings, payload.ClearSecrets)
	}
	if err != nil {
		h.writePluginError(c, "plugin_settings", err, c.Param("id"))
		return
	}
	h.persistState()
	metadata := pluginLogMetadata(state)
	metadata["profile_id"] = profileID
	recordRequestOperation(c, h.logs, "plugin_settings", "机器人插件设置已更新", state.Manifest.ID, metadata)
	c.JSON(http.StatusOK, state.Redacted())
}

func (h *BotHandler) testMusicConnections(c *gin.Context) {
	profileID, scopeOK := h.pluginProfileScope(c)
	if !scopeOK {
		return
	}
	var payload musicConnectionTestPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "plugin_music_test", err, "official.music", nil)
		return
	}
	plugin, settings, ok := h.runtime.Plugins().PluginForConfiguration("official.music", profileID)
	if !ok {
		h.writeError(c, http.StatusNotFound, "plugin_music_test", assistant.ErrPluginNotFound, "official.music", nil)
		return
	}
	music, ok := plugin.(*assistant.MusicPlugin)
	if !ok {
		h.writeError(c, http.StatusInternalServerError, "plugin_music_test", errors.New("music plugin has unexpected implementation"), "official.music", nil)
		return
	}
	for key, value := range payload.Settings {
		if text, secret := value.(string); secret && strings.TrimSpace(text) == "" && strings.HasSuffix(key, "_cookie") {
			continue
		}
		settings[key] = value
	}
	for _, key := range payload.ClearSecrets {
		delete(settings, strings.TrimSpace(key))
	}
	c.JSON(http.StatusOK, gin.H{"sources": music.TestConnections(c.Request.Context(), settings)})
}

// writePluginError 按插件错误类型返回合适的 HTTP 状态码。
func (h *BotHandler) writePluginError(c *gin.Context, action string, err error, target string) {
	if errors.Is(err, assistant.ErrPluginNotFound) {
		h.writeError(c, http.StatusNotFound, action, err, target, map[string]any{"plugin_id": target})
		return
	}
	h.writeError(c, http.StatusBadRequest, action, err, target, map[string]any{"plugin_id": target})
}

// writeError 写出统一 JSON 错误响应。
func (h *BotHandler) writeError(c *gin.Context, status int, action string, err error, target string, metadata map[string]any) {
	logAndWriteError(c, h.logs, status, action, err, target, metadata)
}

// persistState 将插件状态写入 SQLite。
func (h *BotHandler) persistState() {
	if h.sqlite == nil {
		return
	}
	// 插件开关/安装状态不在 runtime.Config 里，因此单独持久化。
	if err := h.sqlite.SavePluginStates(h.ctx, h.runtime.Plugins().Snapshot()); err != nil {
		recordError(h.ctx, h.logs, "persist", err, "plugin_states", nil)
	}
}

// botLogMetadata 构造 OneBot v11 机器人操作日志的附加信息。
func botLogMetadata(cfg assistant.BotConfig) map[string]any {
	return map[string]any{
		"profile_id":              cfg.ID,
		"profile_name":            cfg.Name,
		"platform":                cfg.Platform,
		"enabled":                 cfg.Enabled,
		"onebot_reverse_ws":       cfg.OneBotReverseWSEndpoint,
		"nonebot_bridge_enabled":  cfg.NoneBotBridgeEnabled,
		"nonebot_bridge_endpoint": cfg.NoneBotBridgeEndpoint,
		"bot_qq":                  cfg.BotAccount,
		"owner_id":                cfg.OwnerID,
	}
}

// pluginLogMetadata 构造插件操作日志的附加信息。
func pluginLogMetadata(state assistant.PluginState) map[string]any {
	return map[string]any{
		"plugin_id": state.Manifest.ID,
		"name":      state.Manifest.Name,
		"installed": state.Installed,
		"enabled":   state.Enabled,
		"official":  state.Manifest.Official,
	}
}

// groupEvents 从运行时最近事件里筛出指定群的收发记录。
func groupEvents(events []assistant.EventRecord, groupID string) []assistant.EventRecord {
	groupID = strings.TrimSpace(groupID)
	out := make([]assistant.EventRecord, 0, len(events))
	for _, event := range events {
		if event.GroupID == groupID {
			out = append(out, event)
		}
	}
	return out
}

func oneBotMessageID(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	value, ok := data["message_id"]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

// existingBotProfileConfig 找出 payload 编辑的是哪台机器人。老客户端不带 ID 时，
// 只有一台机器人才能确定是它；多台时当作新配置，不替它猜。
func existingBotProfileConfig(set assistant.ProfileSet, payload assistant.ConfigPayload) assistant.BotConfig {
	set = set.WithDefaults()
	if profile, ok := set.ConfigForProfile(payload.ID); ok {
		return profile.WithDefaults()
	}
	if strings.TrimSpace(payload.ID) == "" && len(set.Profiles) == 1 {
		return set.Profiles[0].WithDefaults()
	}
	return assistant.DefaultBotConfig()
}

// upsertBotProfileSet 把表单保存进配置集，返回新配置集和这台机器人的 ID。
func upsertBotProfileSet(set assistant.ProfileSet, payload assistant.ConfigPayload, cfg assistant.BotConfig) (assistant.ProfileSet, string) {
	set = set.WithDefaults()
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		targetID = strings.TrimSpace(cfg.ID)
	}
	cfg = cfg.WithDefaults()
	if targetID == "" {
		targetID = assistant.NewProfileSet(cfg).Profiles[0].ID
	}
	cfg.ID = targetID
	for i := range set.Profiles {
		if set.Profiles[i].ID != targetID {
			continue
		}
		set.Profiles[i] = cfg
		return set.WithDefaults(), targetID
	}
	set.Profiles = append(set.Profiles, cfg)
	return set.WithDefaults(), targetID
}

// runtimeLogMetadata 描述整个运行时的操作（启停、回补）涉及哪些机器人：这些操作不属于
// 某一台，日志里列出全部启用的那几台。
func (h *BotHandler) runtimeLogMetadata() map[string]any {
	var enabled []string
	for _, profile := range h.runtime.ProfileConfigs() {
		if profile.Enabled {
			enabled = append(enabled, profile.ID)
		}
	}
	return map[string]any{"enabled_profiles": enabled}
}

// botLogTarget 选择更适合日志索引的机器人配置目标。
func botLogTarget(cfg assistant.BotConfig) string {
	for _, value := range []string{cfg.ID, cfg.BotAccount, cfg.OneBotReverseWSEndpoint, cfg.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
