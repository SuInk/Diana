package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type QQBotRuntime interface {
	Start(context.Context) error
	Stop() error
	UpdateConfig(context.Context, assistant.BotConfig, assistant.Channel) error
	Config() assistant.BotConfig
	Status() assistant.RuntimeStatus
	CallOneBotAPI(context.Context, string, map[string]any) (map[string]any, error)
	SendGroupMessage(context.Context, string, string) (map[string]any, error)
	Plugins() *assistant.PluginManager
}

type QQBotChannelFactory func(assistant.BotConfig) assistant.Channel

type QQBotHandler struct {
	runtime                   QQBotRuntime
	newChannel                QQBotChannelFactory
	ctx                       context.Context
	profiles                  QQBotProfileStore
	groupConfigs              QQBotGroupConfigStore
	groupAdmin                *groupAdminVerifier
	localMedia                assistant.LocalMediaSharer
	sqlite                    *storage.SQLiteStore
	logs                      AppLogWriter
	features                  QQBotFeatureFlags
	installResolverDependency func(context.Context, string) (assistant.ResolverDependencyInstallResult, error)
}

type QQBotFeatureFlags struct {
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

const minQQBotTokenChars = 16

// NewQQBotHandler 创建 QQBotHandler 实例。
func NewQQBotHandler(ctx context.Context, runtime QQBotRuntime) *QQBotHandler {
	return NewQQBotHandlerWithFactory(ctx, runtime, func(cfg assistant.BotConfig) assistant.Channel {
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

// NewQQBotHandlerWithFactory 创建 QQBotHandler 实例。
func NewQQBotHandlerWithFactory(ctx context.Context, runtime QQBotRuntime, factory QQBotChannelFactory) *QQBotHandler {
	return &QQBotHandler{
		runtime:                   runtime,
		newChannel:                factory,
		ctx:                       ctx,
		installResolverDependency: assistant.InstallResolverDependency,
		// 没有显式持久化 store 时，至少保证本次进程内也能按配置集语义工作。
		profiles:     NewMemoryQQBotProfileStore(runtime.Config()),
		groupConfigs: NewMemoryQQBotGroupConfigStore(),
		groupAdmin:   newGroupAdminVerifier(),
	}
}

// SetFeatureFlags 配置只应在显式测试环境开放的 WebUI 功能。
func (h *QQBotHandler) SetFeatureFlags(flags QQBotFeatureFlags) {
	h.features = flags
}

// SetLocalMediaSharer lets OneBot fetch local diagnostic files over HTTP.
func (h *QQBotHandler) SetLocalMediaSharer(sharer assistant.LocalMediaSharer) {
	h.localMedia = sharer
}

// SetProfileStore 注入 QQ 机器人配置集存储。
func (h *QQBotHandler) SetProfileStore(store QQBotProfileStore) {
	if store == nil {
		return
	}
	h.profiles = store
}

// SetGroupConfigStore 注入 QQ 群级配置存储。
func (h *QQBotHandler) SetGroupConfigStore(store QQBotGroupConfigStore) {
	if store == nil {
		return
	}
	h.groupConfigs = store
}

// SetSQLiteStore 注入 SQLite，用于插件状态持久化和操作日志。
func (h *QQBotHandler) SetSQLiteStore(store *storage.SQLiteStore) {
	h.sqlite = store
	h.logs = store
}

// Register registers the generic assistant API and its legacy QQ-only alias.
func (h *QQBotHandler) Register(router gin.IRouter) {
	h.registerRoutes(router, "/api/assistant")
	h.registerRoutes(router, "/api/qqbot")
	// 控制台登录用户直接管理全部群配置，无需群验证码流程。
	h.registerConsoleGroupRoutes(router)
}

func (h *QQBotHandler) registerRoutes(router gin.IRouter, base string) {
	router.GET(base+"/config", h.getConfig)
	router.GET(base+"/platforms", h.platforms)
	router.POST(base+"/config", h.saveConfig)
	router.POST(base+"/config/activate", h.activateProfile)
	router.POST(base+"/config/clone", h.cloneProfile)
	router.POST(base+"/config/delete", h.deleteProfile)
	router.GET(base+"/features", h.featuresStatus)
	router.GET(base+"/status", h.status)
	router.GET(base+"/auto-info", h.autoInfo)
	router.GET(base+"/dashboard-stats", h.dashboardStats)
	router.GET(base+"/tasks", h.listTasks)
	router.POST(base+"/start", h.start)
	router.POST(base+"/stop", h.stop)
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
	router.POST(base+"/group-admin/challenge", h.startGroupAdminChallenge)
	router.POST(base+"/group-admin/verify", h.verifyGroupAdminChallenge)
	router.GET(base+"/group-admin/config", h.getGroupAdminConfig)
	router.POST(base+"/group-admin/config", h.saveGroupAdminConfig)
}

// platforms 返回可用于创建机器人配置的平台及协议适配器。
func (h *QQBotHandler) platforms(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"platforms": assistant.SupportedPlatforms()})
}

// getConfig 处理 QQ 机器人配置读取请求。
func (h *QQBotHandler) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(h.profiles.Profiles()))
}

// featuresStatus 返回当前 WebUI 暴露的 QQ 机器人测试能力。
func (h *QQBotHandler) featuresStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.features)
}

// saveConfig 保存当前机器人配置或新增机器人配置档。
func (h *QQBotHandler) saveConfig(c *gin.Context) {
	var payload assistant.ConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, "", nil)
		return
	}

	set := h.profiles.Profiles()
	existing := existingQQBotProfileConfig(set, payload)
	cfg := assistant.ConfigFromPayload(payload, existing)
	if err := validateTokenLength("onebot_access_token", payload.OneBotAccessToken); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, qqbotLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := validateTokenLength("nonebot_bridge_token", payload.NoneBotBridgeToken); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, qqbotLogTarget(cfg), botLogMetadata(cfg))
		return
	}
	if err := cfg.Validate(); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, qqbotLogTarget(cfg), botLogMetadata(cfg))
		return
	}

	next := upsertQQBotProfileSet(set, payload, cfg)
	current, ok := next.Current()
	if !ok {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", fmt.Errorf("qqbot profile set is empty"), "", nil)
		return
	}
	// 当前激活机器人配置发生变化时，运行时要同步切换并按需重启连接。
	if err := h.runtime.UpdateConfig(h.ctx, current, h.newChannel(current)); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.config.save", err, qqbotLogTarget(current), botLogMetadata(current))
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.config.save", "QQ 机器人配置已保存", current.ID, botLogMetadata(current))
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

// activateProfile 切换当前激活的 QQ 机器人配置档。
func (h *QQBotHandler) activateProfile(c *gin.Context) {
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
	if err := h.runtime.UpdateConfig(h.ctx, current, h.newChannel(current)); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.activate", err, qqbotLogTarget(current), botLogMetadata(current))
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.profile.activate", "QQ 机器人配置已切换", targetID, botLogMetadata(current))
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

// cloneProfile 复制指定 QQ 机器人配置档。
func (h *QQBotHandler) cloneProfile(c *gin.Context) {
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
		next := upsertQQBotProfileSet(set, assistant.ConfigPayload{Name: cloned.Name}, cloned)
		current, ok := next.Current()
		if !ok {
			h.writeError(c, http.StatusBadRequest, "assistant.profile.clone", fmt.Errorf("qqbot profile set is empty"), "", nil)
			return
		}
		if err := h.runtime.UpdateConfig(h.ctx, current, h.newChannel(current)); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
			h.writeError(c, http.StatusBadRequest, "assistant.profile.clone", err, qqbotLogTarget(current), botLogMetadata(current))
			return
		}
		h.profiles.SaveProfiles(next)
		recordRequestOperation(c, h.logs, "assistant.profile.clone", "QQ 机器人配置已复制", sourceID, botLogMetadata(profile))
		c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
		return
	}
	h.writeError(c, http.StatusNotFound, "assistant.profile.clone", fmt.Errorf("profile %q not found", sourceID), sourceID, nil)
}

// deleteProfile 删除指定 QQ 机器人配置档。
func (h *QQBotHandler) deleteProfile(c *gin.Context) {
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
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", fmt.Errorf("qqbot profile set is empty"), "", nil)
		return
	}
	if err := h.runtime.UpdateConfig(h.ctx, current, h.newChannel(current)); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		h.writeError(c, http.StatusBadRequest, "assistant.profile.delete", err, qqbotLogTarget(current), botLogMetadata(current))
		return
	}
	h.profiles.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "assistant.profile.delete", "QQ 机器人配置已删除", targetID, map[string]any{"profile_id": targetID})
	c.JSON(http.StatusOK, assistant.PayloadFromProfileSet(next))
}

// validateTokenLength 校验用户显式填写的 token 长度。
func validateTokenLength(field string, value string) error {
	// 空 token 表示不鉴权或沿用旧值；只有用户显式填写时才检查强度。
	if value == "" {
		return nil
	}
	if utf8.RuneCountInString(value) < minQQBotTokenChars {
		return fmt.Errorf("%s must be at least %d characters", field, minQQBotTokenChars)
	}
	return nil
}

// status 返回 QQ 机器人运行状态快照。
func (h *QQBotHandler) status(c *gin.Context) {
	c.JSON(http.StatusOK, h.runtime.Status())
}

// start 处理启动 QQ 机器人的请求。
func (h *QQBotHandler) start(c *gin.Context) {
	if err := h.runtime.Start(h.ctx); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.start", err, qqbotLogTarget(h.runtime.Config()), botLogMetadata(h.runtime.Config()))
		return
	}
	recordRequestOperation(c, h.logs, "assistant.start", "QQ 机器人已启动", h.runtime.Config().ID, botLogMetadata(h.runtime.Config()))
	c.JSON(http.StatusOK, h.runtime.Status())
}

// stop 处理停止 QQ 机器人的请求。
func (h *QQBotHandler) stop(c *gin.Context) {
	if err := h.runtime.Stop(); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.stop", err, qqbotLogTarget(h.runtime.Config()), botLogMetadata(h.runtime.Config()))
		return
	}
	recordRequestOperation(c, h.logs, "assistant.stop", "QQ 机器人已停止", h.runtime.Config().ID, botLogMetadata(h.runtime.Config()))
	c.JSON(http.StatusOK, h.runtime.Status())
}

// getGroupTest 返回指定群最近收发事件，辅助真实 QQ 群联调。
func (h *QQBotHandler) getGroupTest(c *gin.Context) {
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

// sendGroupTest 通过当前 OneBot 连接向 QQ 群发送测试消息，并返回近期收到的同群事件。
func (h *QQBotHandler) sendGroupTest(c *gin.Context) {
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
	recordRequestOperation(c, h.logs, "assistant.group_test.send", "QQ群测试消息已发送", groupID, map[string]any{
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
func (h *QQBotHandler) listPlugins(c *gin.Context) {
	c.JSON(http.StatusOK, assistant.RedactStates(h.runtime.Plugins().List()))
}

// pluginDependencies 返回解析器外部依赖的探测结果，让控制台能直接看出
// yt-dlp / ffmpeg / node 是否齐全，而不是等用户发链接后才报错。
func (h *QQBotHandler) pluginDependencies(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"resolver": assistant.RefreshResolverDependencies()})
}

// installPluginDependency 安装链接解析插件白名单中的外部命令。
func (h *QQBotHandler) installPluginDependency(c *gin.Context) {
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
func (h *QQBotHandler) installPlugin(c *gin.Context) {
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
func (h *QQBotHandler) uninstallPlugin(c *gin.Context) {
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
func (h *QQBotHandler) setPluginEnabled(c *gin.Context) {
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
func (h *QQBotHandler) updatePluginSettings(c *gin.Context) {
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
func (h *QQBotHandler) writePluginError(c *gin.Context, action string, err error, target string) {
	if errors.Is(err, assistant.ErrPluginNotFound) {
		h.writeError(c, http.StatusNotFound, action, err, target, map[string]any{"plugin_id": target})
		return
	}
	h.writeError(c, http.StatusBadRequest, action, err, target, map[string]any{"plugin_id": target})
}

// writeError 写出统一 JSON 错误响应。
func (h *QQBotHandler) writeError(c *gin.Context, status int, action string, err error, target string, metadata map[string]any) {
	logAndWriteError(c, h.logs, status, action, err, target, metadata)
}

// persistState 将插件状态写入 SQLite。
func (h *QQBotHandler) persistState() {
	if h.sqlite == nil {
		return
	}
	// 插件开关/安装状态不在 runtime.Config 里，因此单独持久化。
	if err := h.sqlite.SavePluginStates(h.ctx, h.runtime.Plugins().Snapshot()); err != nil {
		recordError(h.ctx, h.logs, "assistant.persist", err, "plugin_states", nil)
	}
}

// botLogMetadata 构造 QQ 机器人操作日志的附加信息。
func botLogMetadata(cfg assistant.BotConfig) map[string]any {
	return map[string]any{
		"profile_id":              cfg.ID,
		"profile_name":            cfg.Name,
		"platform":                cfg.Platform,
		"enabled":                 cfg.Enabled,
		"onebot_reverse_ws":       cfg.OneBotReverseWSEndpoint,
		"nonebot_bridge_enabled":  cfg.NoneBotBridgeEnabled,
		"nonebot_bridge_endpoint": cfg.NoneBotBridgeEndpoint,
		"bot_qq":                  cfg.BotQQ,
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

// existingQQBotProfileConfig 根据 payload 推断“编辑的是哪个机器人配置档”。
func existingQQBotProfileConfig(set assistant.ProfileSet, payload assistant.ConfigPayload) assistant.BotConfig {
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

// upsertQQBotProfileSet 把当前表单保存为配置档，并让它成为新的激活机器人。
func upsertQQBotProfileSet(set assistant.ProfileSet, payload assistant.ConfigPayload, cfg assistant.BotConfig) assistant.ProfileSet {
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

// qqbotLogTarget 选择更适合日志索引的机器人配置目标。
func qqbotLogTarget(cfg assistant.BotConfig) string {
	for _, value := range []string{cfg.ID, cfg.BotQQ, cfg.OneBotReverseWSEndpoint, cfg.Name} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
