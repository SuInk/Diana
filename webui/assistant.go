// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	UpdateConfig(context.Context, assistant.BotConfig, assistant.Channel) error
	Config() assistant.BotConfig
	Status() assistant.RuntimeStatus
	CallOneBotAPI(context.Context, string, map[string]any) (map[string]any, error)
	SendGroupMessage(context.Context, string, string) (map[string]any, error)
	Plugins() *assistant.PluginManager
}

type BotChannelFactory func(assistant.BotConfig) assistant.Channel
type BotChannelSetFactory func(assistant.ProfileSet) assistant.Channel

type profileAwareRuntime interface {
	SetProfiles(assistant.ProfileSet)
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
	liveGroupMu               sync.Mutex
	liveGroupCache            liveGroupListCache
}

type BotFeatureFlags struct {
	GroupTest bool `json:"group_test"`
}

type pluginEnabledPayload struct {
	Enabled bool `json:"enabled"`
}

type pluginSettingsPayload struct {
	// Settings 是要保存的覆盖值全集，空 map 表示恢复默认。
	Settings map[string]any `json:"settings"`
	// ClearSecrets 列出要显式清除的凭据键。凭据不会因为没提交或提交空串
	// 而被清空，只有出现在这里才会真的删掉。
	ClearSecrets []string `json:"clear_secrets,omitempty"`
}

type groupTestPayload struct {
	GroupID string `json:"group_id"`
	Message string `json:"message"`
}

type groupAdminChallengePayload struct {
	GroupID string `json:"group_id"`
	UserID  string `json:"user_id"`
}

type groupAdminChallengeResponse struct {
	GroupID   string    `json:"group_id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Message   string    `json:"message"`
}

type groupAdminVerifyPayload struct {
	GroupID string `json:"group_id"`
	UserID  string `json:"user_id"`
	Code    string `json:"code"`
}

type groupAdminSessionPayload struct {
	Token  string                `json:"token"`
	Config assistant.GroupConfig `json:"config,omitempty"`
}

type groupAdminConfigResponse struct {
	GroupID   string                  `json:"group_id"`
	UserID    string                  `json:"user_id,omitempty"`
	Token     string                  `json:"token,omitempty"`
	ExpiresAt time.Time               `json:"expires_at,omitempty"`
	Config    assistant.GroupConfig   `json:"config"`
	Plugins   []assistant.PluginState `json:"plugins"`
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

const minBotTokenChars = 16

// NewBotHandler 创建 BotHandler 实例。
func NewBotHandler(ctx context.Context, runtime BotRuntime) *BotHandler {
	return NewBotHandlerWithFactory(ctx, runtime, func(cfg assistant.BotConfig) assistant.Channel {
		if cfg.Platform == assistant.PlatformTelegram {
			return assistant.NewTelegramChannel(assistant.TelegramConfig{
				BotToken:   cfg.TelegramBotToken,
				APIBaseURL: cfg.TelegramAPIBaseURL,
				ProxyURL:   cfg.TelegramProxyURL,
			})
		}
		return assistant.NewOneBotReverseServer(assistant.OneBotConfig{
			Endpoint:    cfg.OneBotReverseWSEndpoint,
			AccessToken: cfg.OneBotAccessToken,
		})
	})
}

// NewBotHandlerWithFactory 创建 BotHandler 实例。
func NewBotHandlerWithFactory(ctx context.Context, runtime BotRuntime, factory BotChannelFactory) *BotHandler {
	return &BotHandler{
		runtime:                   runtime,
		newChannel:                factory,
		ctx:                       ctx,
		installResolverDependency: assistant.InstallResolverDependency,
		// 没有显式持久化 store 时，至少保证本次进程内也能按配置集语义工作。
		profiles:     NewMemoryBotProfileStore(runtime.Config()),
		groupConfigs: NewMemoryBotGroupConfigStore(),
		groupAdmin:   newGroupAdminVerifier(),
	}
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
}

// SetSQLiteStore 注入 SQLite，用于插件状态持久化和操作日志。
func (h *BotHandler) SetSQLiteStore(store *storage.SQLiteStore) {
	h.sqlite = store
	h.logs = store
}

// Register registers the generic assistant API and its legacy single-platform alias.
func (h *BotHandler) Register(router gin.IRouter) {
	h.registerRoutes(router, "/api/assistant")
	h.registerRoutes(router, "/api/qqbot")
	// 控制台登录用户直接管理全部群配置，无需群验证码流程。
	h.registerConsoleGroupRoutes(router)
}

func (h *BotHandler) registerRoutes(router gin.IRouter, base string) {
	router.GET(base+"/config", h.getConfig)
	router.GET(base+"/platforms", h.platforms)
	router.POST(base+"/config", h.saveConfig)
	router.POST(base+"/config/activate", h.activateProfile)
	router.POST(base+"/config/clone", h.cloneProfile)
	router.POST(base+"/config/delete", h.deleteProfile)
	router.POST(base+"/config/context-isolation", h.setContextIsolation)
	router.GET(base+"/features", h.featuresStatus)
	router.GET(base+"/status", h.status)
	router.GET(base+"/auto-info", h.autoInfo)
	router.GET(base+"/dashboard-stats", h.dashboardStats)
	router.GET(base+"/events", h.listEvents)
	router.GET(base+"/events/:id/trace", h.eventTrace)
	router.GET(base+"/events/:id/images/:index", h.eventImage)
	router.GET(base+"/users", h.listAssistantUsers)
	router.GET(base+"/users/:id", h.getAssistantUser)
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
	router.GET(base+"/plugins/dependencies", h.pluginDependencies)
	router.POST(base+"/plugins/dependencies/:name/install", h.installPluginDependency)
	router.POST(base+"/plugins/:id/install", h.installPlugin)
	router.POST(base+"/plugins/:id/uninstall", h.uninstallPlugin)
	router.POST(base+"/plugins/:id/enabled", h.setPluginEnabled)
	router.POST(base+"/plugins/:id/settings", h.updatePluginSettings)
	router.POST(base+"/plugins/repository-publish/issues", h.createRepositoryIssue)
	router.GET(base+"/plugins/repository-publish/drafts", h.listRepositoryIssueDrafts)
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
func (h *BotHandler) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(h.profiles.Profiles()))
}

// featuresStatus 返回当前 WebUI 暴露的 OneBot v11 机器人测试能力。
func (h *BotHandler) featuresStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.features)
}

// saveConfig 保存当前机器人配置或新增机器人配置档。
func (h *BotHandler) saveConfig(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, "", nil)
		return
	}

	set := h.profiles.Profiles()
	existing := existingBotProfileConfig(set, payload)
	cfg := assistant.ConfigFromPayload(payload, existing)
	if err := validateTokenLength("onebot_access_token", payload.OneBotAccessToken); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := validateTokenLength("nonebot_bridge_token", payload.NoneBotBridgeToken); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := cfg.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, botLogTarget(cfg), botLogMetadata(cfg))
		return
	}

	next := upsertBotProfileSet(set, payload, cfg)
	current, ok := next.Current()
	if !ok {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", fmt.Errorf("chatbot profile set is empty"), "", nil)
		return
	}
	// 当前激活机器人配置发生变化时，运行时要同步切换并按需重启连接。
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, botLogTarget(current), botLogMetadata(current))
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.config.save", "OneBot v11 机器人配置已保存", current.ID, botLogMetadata(current))
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

// activateProfile 切换当前激活的 OneBot v11 机器人配置档。
func (h *BotHandler) activateProfile(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.activate", err, "", nil)
		return
	}
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.activate", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	next := h.profiles.Profiles().WithActive(targetID)
	current, ok := next.Current()
	if !ok || current.ID != targetID {
		h.writeError(c, http.StatusNotFound, "assistant.profile.activate", fmt.Errorf("profile %q not found", targetID), targetID, nil)
		return
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.activate", err, botLogTarget(current), botLogMetadata(current))
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.profile.activate", "OneBot v11 机器人配置已切换", targetID, botLogMetadata(current))
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

// cloneProfile 复制指定 OneBot v11 机器人配置档。
func (h *BotHandler) cloneProfile(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.clone", err, "", nil)
		return
	}
	sourceID := strings.TrimSpace(payload.ID)
	set := h.profiles.Profiles()
	if sourceID == "" {
		sourceID = set.ActiveID
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
		next := upsertBotProfileSet(set, assistant.ConfigPayload{Name: cloned.Name}, cloned)
		current, ok := next.Current()
		if !ok {
			h.writeError(c, http.StatusBadRequest, "assistant.profile.clone", fmt.Errorf("chatbot profile set is empty"), "", nil)
			return
		}
		if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
			h.writeError(c, http.StatusBadRequest, "assistant.profile.clone", err, botLogTarget(current), botLogMetadata(current))
			return
		}
		h.profiles.SaveProfiles(next)
		recordRequestOperation(c, h.logs, "assistant.profile.clone", "OneBot v11 机器人配置已复制", sourceID, botLogMetadata(profile))
		c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
		return
	}
	h.writeError(c, http.StatusNotFound, "assistant.profile.clone", fmt.Errorf("profile %q not found", sourceID), sourceID, nil)
}

// deleteProfile 删除指定 OneBot v11 机器人配置档。
func (h *BotHandler) deleteProfile(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", err, "", nil)
		return
	}
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	set := h.profiles.Profiles()
	if len(set.Profiles) <= 1 {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", fmt.Errorf("at least one qqbot profile must remain"), targetID, nil)
		return
	}
	next := set.Delete(targetID)
	if len(next.Profiles) == len(set.Profiles) {
		h.writeError(c, http.StatusNotFound, "assistant.profile.delete", fmt.Errorf("profile %q not found", targetID), targetID, nil)
		return
	}
	current, ok := next.Current()
	if !ok {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", fmt.Errorf("chatbot profile set is empty"), "", nil)
		return
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", err, botLogTarget(current), botLogMetadata(current))
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.profile.delete", "OneBot v11 机器人配置已删除", targetID, map[string]any{"profile_id": targetID})
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

type contextIsolationPayload struct {
	Enabled bool `json:"enabled"`
}

func (h *BotHandler) setContextIsolation(c *gin.Context) {
	var payload contextIsolationPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.context_isolation.update", err, "", nil)
		return
	}
	next := h.profiles.Profiles().WithPlatformContextIsolation(payload.Enabled)
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.context_isolation.update", err, "", nil)
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.context_isolation.update", "平台上下文隔离设置已更新", "", map[string]any{"enabled": payload.Enabled})
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

func (h *BotHandler) applyProfileSet(set assistant.ProfileSet) error {
	set = set.WithDefaults()
	cfg, ok := set.RuntimeConfig()
	if !ok {
		return fmt.Errorf("assistant profile set is empty")
	}
	if runtime, ok := h.runtime.(profileAwareRuntime); ok {
		runtime.SetProfiles(set)
	}
	channel := h.newChannel(cfg)
	if h.newChannelSet != nil {
		channel = h.newChannelSet(set)
	}
	return h.runtime.UpdateConfig(h.ctx, cfg, channel)
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
		h.writeError(c, http.StatusBadRequest, "assistant.start", err, botLogTarget(h.runtime.Config()), botLogMetadata(h.runtime.Config()))
		return
	}
	recordRequestOperation(c, h.logs, "assistant.start", "OneBot v11 机器人已启动", h.runtime.Config().ID, botLogMetadata(h.runtime.Config()))
	c.JSON(http.StatusOK, h.runtime.Status())
}

// stop 处理停止 OneBot v11 机器人的请求。
func (h *BotHandler) stop(c *gin.Context) {
	if err := h.runtime.Stop(); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.stop", err, botLogTarget(h.runtime.Config()), botLogMetadata(h.runtime.Config()))
		return
	}
	recordRequestOperation(c, h.logs, "assistant.stop", "OneBot v11 机器人已停止", h.runtime.Config().ID, botLogMetadata(h.runtime.Config()))
	c.JSON(http.StatusOK, h.runtime.Status())
}

// requestBackfill 手动触发一次历史消息回补，用于实测回补效果。
func (h *BotHandler) requestBackfill(c *gin.Context) {
	runtime, ok := h.runtime.(historyBackfillRuntime)
	if !ok {
		h.writeError(c, http.StatusNotImplemented, "assistant.backfill", fmt.Errorf("runtime does not support manual history backfill"), botLogTarget(h.runtime.Config()), botLogMetadata(h.runtime.Config()))
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
		h.writeError(c, http.StatusConflict, "assistant.backfill", err, botLogTarget(h.runtime.Config()), botLogMetadata(h.runtime.Config()))
		return
	}
	recordRequestOperation(c, h.logs, "assistant.backfill", fmt.Sprintf("已触发手动回补，窗口 %s", window), h.runtime.Config().ID, botLogMetadata(h.runtime.Config()))
	c.JSON(http.StatusOK, gin.H{"requested": true, "window_hours": window.Hours()})
}

// getGroupTest 返回指定群最近收发事件，辅助真实 群联调。
func (h *BotHandler) getGroupTest(c *gin.Context) {
	groupID := strings.TrimSpace(c.Query("group_id"))
	if groupID == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.status", fmt.Errorf("group_id is required"), "", nil)
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
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.send", err, "", nil)
		return
	}
	groupID := strings.TrimSpace(payload.GroupID)
	message := strings.TrimSpace(payload.Message)
	if groupID == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.send", fmt.Errorf("group_id is required"), "", nil)
		return
	}
	if message == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.send", fmt.Errorf("message is required"), groupID, map[string]any{"group_id": groupID})
		return
	}
	sendResult, err := h.runtime.SendGroupMessage(c.Request.Context(), groupID, message)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.send", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	messageID := oneBotMessageID(sendResult)
	status := h.runtime.Status()
	recordRequestOperation(c, h.logs, "assistant.group_test.send", "群测试消息已发送", groupID, map[string]any{
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

// listPlugins 返回机器人插件列表。
func (h *BotHandler) listPlugins(c *gin.Context) {
	c.JSON(http.StatusOK, assistant.RedactStates(h.runtime.Plugins().ListVisible()))
}

// pluginDependencies 返回各插件外部依赖的探测结果，让控制台能直接看出
// yt-dlp / ffmpeg / node / 浏览器是否齐全，而不是等用户发链接后才报错。
//
// plugins 按插件 ID 分组，界面据此决定在哪张卡片上显示；resolver 保留原样，
// 安装接口的返回体还在用它。
func (h *BotHandler) pluginDependencies(c *gin.Context) {
	resolver := assistant.ResolverDependencies()
	browser := assistant.BrowserDependencies()
	if queryBool(c.Query("refresh")) {
		resolver = assistant.RefreshResolverDependencies()
		browser = assistant.RefreshBrowserDependencies()
	}
	c.JSON(http.StatusOK, gin.H{
		"resolver": resolver,
		"plugins": gin.H{
			assistant.ResolverPluginID:         resolver,
			assistant.SandboxedBrowserPluginID: browser,
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
		h.writeError(c, status, "assistant.plugin.dependency.install", err, name, map[string]any{"dependency": name})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.plugin.dependency.install", "链接解析运行依赖已安装", name, map[string]any{
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
		h.writePluginError(c, "assistant.plugin.install", err, c.Param("id"))
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "assistant.plugin.install", "机器人插件已安装", state.Manifest.ID, pluginLogMetadata(state))
	c.JSON(http.StatusOK, state.Redacted())
}

// uninstallPlugin 处理插件卸载请求。
func (h *BotHandler) uninstallPlugin(c *gin.Context) {
	state, err := h.runtime.Plugins().Uninstall(c.Param("id"))
	if err != nil {
		h.writePluginError(c, "assistant.plugin.uninstall", err, c.Param("id"))
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "assistant.plugin.uninstall", "机器人插件已卸载", state.Manifest.ID, pluginLogMetadata(state))
	c.JSON(http.StatusOK, state.Redacted())
}

// setPluginEnabled 处理插件启用状态变更请求。
func (h *BotHandler) setPluginEnabled(c *gin.Context) {
	var payload pluginEnabledPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.enabled", err, c.Param("id"), map[string]any{"plugin_id": c.Param("id")})
		return
	}
	state, err := h.runtime.Plugins().SetEnabled(c.Param("id"), payload.Enabled)
	if err != nil {
		h.writePluginError(c, "assistant.plugin.enabled", err, c.Param("id"))
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "assistant.plugin.enabled", "机器人插件开关已更新", state.Manifest.ID, pluginLogMetadata(state))
	c.JSON(http.StatusOK, state.Redacted())
}

// updatePluginSettings 处理插件详细设置变更请求。
func (h *BotHandler) updatePluginSettings(c *gin.Context) {
	var payload pluginSettingsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.settings", err, c.Param("id"), map[string]any{"plugin_id": c.Param("id")})
		return
	}
	state, err := h.runtime.Plugins().UpdateSettingsWithClears(c.Param("id"), payload.Settings, payload.ClearSecrets)
	if err != nil {
		h.writePluginError(c, "assistant.plugin.settings", err, c.Param("id"))
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "assistant.plugin.settings", "机器人插件设置已更新", state.Manifest.ID, pluginLogMetadata(state))
	c.JSON(http.StatusOK, state.Redacted())
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
		recordError(h.ctx, h.logs, "assistant.persist", err, "plugin_states", nil)
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

// existingBotProfileConfig 根据 payload 推断“编辑的是哪个机器人配置档”。
func existingBotProfileConfig(set assistant.ProfileSet, payload assistant.ConfigPayload) assistant.BotConfig {
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		targetID = strings.TrimSpace(payload.ActiveProfileID)
	}
	if targetID == "" {
		targetID = strings.TrimSpace(set.ActiveID)
	}
	for _, profile := range set.WithDefaults().Profiles {
		if profile.ID == targetID {
			return profile.WithDefaults()
		}
	}
	if current, ok := set.Current(); ok {
		return current.WithDefaults()
	}
	return assistant.DefaultBotConfig()
}

// upsertBotProfileSet 把当前表单保存为配置档，并让它成为新的激活机器人。
func upsertBotProfileSet(set assistant.ProfileSet, payload assistant.ConfigPayload, cfg assistant.BotConfig) assistant.ProfileSet {
	set = set.WithDefaults()
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		targetID = strings.TrimSpace(cfg.ID)
	}
	cfg = cfg.WithDefaults()
	if targetID == "" {
		targetID = assistant.NewProfileSet(cfg).ActiveID
	}
	cfg.ID = targetID
	for i := range set.Profiles {
		if set.Profiles[i].ID != targetID {
			continue
		}
		set.Profiles[i] = cfg
		set.ActiveID = targetID
		return set.WithDefaults()
	}
	set.Profiles = append(set.Profiles, cfg)
	set.ActiveID = targetID
	return set.WithDefaults()
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
