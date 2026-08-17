// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	// 把 IANA 时区表编进二进制。运行镜像是不带 tzdata 的 alpine，
	// Release 里的裸二进制在 Windows 上也没有系统时区库；缺了它
	// LoadLocation("Asia/Shanghai") 会静默退回 UTC，让按时区配置的
	// 回复时段整体偏移几个小时。
	_ "time/tzdata"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"
	"github.com/SuInk/diana/model/updater"
	"github.com/SuInk/diana/webui"

	"github.com/gin-gonic/gin"
)

// main 初始化存储、路由、机器人运行时并启动 WebUI 服务。
// buildVersion 由构建时 -ldflags "-X main.buildVersion=<version>" 注入；
// Release 使用语义化 tag，普通开发构建使用 dev。
var buildVersion = "dev"

const (
	legacyLLMConfigPluginID = "official.llm-config-skill"
	webSearchPluginID       = "official.web-search"
)

const maxHTTPRequestBodyBytes = 8 << 20

func main() {
	if len(os.Args) == 3 && os.Args[1] == updater.InternalReleaseApplyCommand {
		if err := updater.RunReleaseApplyHelper(os.Args[2]); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "release update failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	logWriter, closeLog := setupLogging()
	defer closeLog()
	probeMacOSQQAppDataAccess()
	port := envOr("PORT", "18080")
	host := envOrAny([]string{"HOST", "BACKEND_HOST"}, "")

	// 所有后台 goroutine 共用这个根 context，收到 Ctrl+C 或 SIGTERM 时统一退出。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := llmConfigFromEnv()

	// SQLite 同时保存模型、助手、插件、提醒和操作日志配置。
	sqliteStore, err := storage.NewSQLiteStore(envOr("APP_DB_PATH", ""))
	if err != nil {
		log.Fatal(err)
	}
	if sqliteStore.Path() != "" {
		_ = os.Setenv("APP_DB_PATH", sqliteStore.Path())
	}
	defer func() {
		_ = sqliteStore.Close()
	}()

	store, err := webui.NewPersistentLLMProfileStore(ctx, sqliteStore, cfg)
	if err != nil {
		log.Fatal(err)
	}
	botProfileStore, err := webui.NewPersistentQQBotProfileStore(ctx, sqliteStore, qqBotConfigFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	botGroupConfigStore, err := webui.NewPersistentQQBotGroupConfigStore(ctx, sqliteStore)
	if err != nil {
		log.Fatal(err)
	}
	reminderStore, err := webui.NewPersistentReminderStore(ctx, sqliteStore)
	if err != nil {
		log.Fatal(err)
	}
	// 模型列表必须从当前 provider 后端读取；公共目录只补全后端常常省略的
	// 模态和 token 限制，失败时保留原列表与“能力未知”状态。
	modelCatalog := llm.NewModelsDevCatalog(nil)
	modelListFactory := func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		models, err := llm.ListModels(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return modelCatalog.Enrich(ctx, cfg, models), nil
	}
	handler := webui.NewLLMConfigHandler(store)
	handler.SetModelListFactory(modelListFactory)
	handler.SetLogStore(sqliteStore)
	systemUpdater, err := newSystemUpdater()
	if err != nil {
		log.Fatal(err)
	}
	systemHandler := webui.NewSystemUpdateHandler(systemUpdater)
	systemHandler.SetLogStore(sqliteStore)
	systemHandler.SetBuildVersion(buildVersion)
	if err := systemHandler.SetUpdatePolicyStore(ctx, sqliteStore); err != nil {
		log.Fatal(err)
	}
	if err := systemHandler.SetReleaseCacheStore(ctx, sqliteStore); err != nil {
		log.Printf("load system release cache: %v", err)
	}
	releaseUpdater, err := updater.NewReleasePackageUpdater(updater.ReleasePackageOptions{
		CurrentVersion: buildVersion,
		FrontendDir:    frontendDistDir(),
		DatabasePath:   sqliteStore.Path(),
		HealthURL:      "http://" + net.JoinHostPort(displayHost(host), port) + "/api/health",
		Arguments:      os.Args[1:],
		Shutdown:       cancel,
		Disable:        !boolFromEnv("DIANA_RELEASE_UPDATE_ENABLED", true),
	})
	if err != nil {
		log.Fatal(err)
	}
	systemHandler.SetReleasePackageUpdater(releaseUpdater)
	systemHandler.StartAutoUpdate(ctx)
	runtimePersistor := webui.NewRuntimePersistor(botProfileStore)
	plugins := assistant.NewDefaultPluginManager()
	if savedPluginStates, ok, err := sqliteStore.LoadPluginStates(ctx); err != nil {
		log.Fatal(err)
	} else if ok {
		statesChanged := false
		if _, exists := savedPluginStates[legacyLLMConfigPluginID]; exists {
			profiles, changed := migrateLegacyLLMConfigPluginState(botProfileStore.Profiles(), savedPluginStates)
			if changed {
				botProfileStore.SaveProfiles(profiles)
			}
			statesChanged = true
		}
		if catalogState, exists := plugins.Get(webSearchPluginID); exists && migrateRestoredWebSearchPluginState(savedPluginStates, catalogState) {
			statesChanged = true
		}
		if statesChanged {
			if err := sqliteStore.SavePluginStates(ctx, savedPluginStates); err != nil {
				log.Fatal(err)
			}
		}
		plugins.Restore(savedPluginStates)
	}
	botSet := botProfileStore.Profiles()
	botCfg, ok := botSet.RuntimeConfig()
	if !ok {
		botCfg = botProfileStore.Current()
	}
	// NapCat 使用反向 WebSocket 连接本服务；这里保留同一个 server 实例，配置变更时只更新 token/endpoint。
	oneBotServer := assistant.NewOneBotReverseServer(assistant.OneBotConfig{
		Endpoint:    botCfg.OneBotReverseWSEndpoint,
		AccessToken: botCfg.OneBotAccessToken,
	})
	channelSetFactory := func(set assistant.ProfileSet) assistant.Channel {
		set = set.WithDefaults()
		bindings := make([]assistant.ChannelBinding, 0, len(set.Profiles))
		oneBotAdded := false
		for _, profile := range set.Profiles {
			profile = profile.WithDefaults()
			if !profile.Enabled {
				continue
			}
			var channel assistant.Channel
			if assistant.IsOneBotPlatform(profile.Platform) {
				// The reverse WebSocket endpoint is process-wide. QQ and Telegram can
				// run together; multiple enabled OneBot profiles still share this one
				// listener, so only the first is attached.
				if oneBotAdded {
					continue
				}
				oneBotAdded = true
				oneBotServer.SetConfig(assistant.OneBotConfig{
					Endpoint:    profile.OneBotReverseWSEndpoint,
					AccessToken: profile.OneBotAccessToken,
				})
				channel = oneBotServer
			} else if profile.Platform == assistant.PlatformTelegram {
				channel = assistant.NewTelegramChannel(assistant.TelegramConfig{
					BotToken:   profile.TelegramBotToken,
					APIBaseURL: profile.TelegramAPIBaseURL,
					ProxyURL:   profile.TelegramProxyURL,
				})
			}
			if channel != nil {
				bindings = append(bindings, assistant.ChannelBinding{
					ProfileID: profile.ID,
					Platform:  profile.Platform,
					Name:      profile.Name,
					Channel:   channel,
				})
			}
		}
		return assistant.NewMultiChannel(bindings, set.PlatformContextsIsolated())
	}
	botRuntime := assistant.NewRuntime(botCfg, channelSetFactory(botSet), plugins, store, reminderStore, runtimePersistor, func() (assistant.LLMProvider, error) {
		return llm.NewClient(store.Current())
	})
	botRuntime.SetProfiles(botSet)
	botRuntime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (assistant.LLMProvider, error) {
		return llm.NewClient(cfg)
	})
	botRuntime.SetGroupConfigStore(botGroupConfigStore)
	botRuntime.SetMessageHistoryStore(sqliteStore)
	botRuntime.SetInboundEventStore(sqliteStore)
	botRuntime.SetUserMemoryStore(sqliteStore)
	botRuntime.SetStructuredMemoryStore(sqliteStore)
	botRuntime.SetRepositoryIssueDraftStore(sqliteStore)
	if err := botRuntime.SetReplySuppressionStore(ctx, sqliteStore); err != nil {
		log.Printf("assistant reply suppression load failed: %v", err)
	}
	botRuntime.SetLLMModelLister(modelListFactory)
	botRuntime.SetAppLogWriter(sqliteStore)
	localMediaBaseURL := envOr(
		"DIANA_LOCAL_MEDIA_BASE_URL",
		"http://"+net.JoinHostPort(displayHost(host), port)+"/media/resolver",
	)
	localMediaStore := assistant.NewLocalMediaStore(localMediaBaseURL)
	if strings.TrimSpace(os.Getenv("DIANA_LOCAL_MEDIA_BASE_URL")) == "" {
		// 未显式配置媒体基址时，按反向 ws 握手时客户端使用的地址动态拼
		// 媒体 URL：桥在容器或别的机器上时（如 host.docker.internal），
		// 能连上 ws 的地址一定也能回源取媒体，用户只需配置 ws 地址。
		localMediaStore.SetOriginProvider(oneBotServer.ConnectionOrigin)
	}
	botRuntime.SetLocalMediaSharer(localMediaStore)
	// 入站图片下载后持久化，识图一律用本地文件的 base64，不依赖模型服务商
	// 能否访问聊天平台那些短时效地址。
	mediaStore := assistant.NewMediaStore(envOr("DIANA_MEDIA_DIR", ""))
	mediaStore.SetLimits(
		int64(envInt("DIANA_MEDIA_MAX_MB", 0))<<20,
		int64(envInt("DIANA_MEDIA_CACHE_MB", 0))<<20,
	)
	botRuntime.SetMediaStore(mediaStore)
	// 先恢复持久统计再挂监听器。配置保存或切换只重启机器人连接，
	// 不会重置这组计数；进程重启也能从去重消息记录恢复基线。
	statsCollector := webui.NewStatsCollector()
	if baseline, err := sqliteStore.DashboardEventStatsSnapshot(ctx, time.Now()); err != nil {
		log.Printf("dashboard stats restore failed: %v", err)
	} else {
		statsCollector.RestoreDurableBaseline(baseline)
	}
	eventHub := webui.NewEventHub()
	botRuntime.SetEventListener(func(event assistant.EventRecord) {
		auditCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := sqliteStore.RecordInboundEventAudit(auditCtx, event); err != nil {
			log.Printf("persist assistant event audit failed: %v", err)
		}
		cancel()
		statsCollector.Observe(event)
		eventHub.PublishBotEvent(event)
	})
	if botCfg.Enabled {
		if err := botRuntime.Start(ctx); err != nil {
			log.Printf("assistant start skipped: %v", err)
		}
	}
	botHandler := webui.NewQQBotHandlerWithFactory(ctx, botRuntime, func(cfg assistant.BotConfig) assistant.Channel {
		if cfg.Platform == assistant.PlatformTelegram {
			return assistant.NewTelegramChannel(assistant.TelegramConfig{
				BotToken:   cfg.TelegramBotToken,
				APIBaseURL: cfg.TelegramAPIBaseURL,
				ProxyURL:   cfg.TelegramProxyURL,
			})
		}
		oneBotServer.SetConfig(assistant.OneBotConfig{
			Endpoint:    cfg.OneBotReverseWSEndpoint,
			AccessToken: cfg.OneBotAccessToken,
		})
		return oneBotServer
	})
	botHandler.SetChannelSetFactory(channelSetFactory)
	botHandler.SetFeatureFlags(webui.QQBotFeatureFlags{
		GroupTest: boolFromEnv("QQBOT_GROUP_TEST_ENABLED", false),
	})
	botHandler.SetLocalMediaSharer(localMediaStore)
	botHandler.SetProfileStore(botProfileStore)
	botHandler.SetGroupConfigStore(botGroupConfigStore)
	botHandler.SetSQLiteStore(sqliteStore)
	logHandler := webui.NewAppLogHandler(sqliteStore)
	napCatLoginHandler, err := webui.NewNapCatLoginHandler(webui.NapCatLoginConfig{
		BaseURL: os.Getenv("DIANA_NAPCAT_WEBUI_URL"),
		Token:   os.Getenv("DIANA_NAPCAT_WEBUI_TOKEN"),
	})
	if err != nil {
		log.Fatal(err)
	}
	statsHandler := webui.NewStatsHandler(statsCollector, botRuntime, sqliteStore.Path())
	eventStreamHandler := webui.NewEventStreamHandler(eventHub, botRuntime, statsCollector, sqliteStore.Path())
	eventStreamHandler.StartWatcher(ctx, 2*time.Second)
	healthHandler := webui.NewHealthHandlerWithVersion(buildVersion)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(limitRequestBody(maxHTTPRequestBodyBytes))
	router.Use(gin.LoggerWithWriter(logWriter), gin.RecoveryWithWriter(logWriter))
	// 鉴权中间件必须在业务路由之前挂载；未设密码时等价于关闭。
	authManager := webui.NewAuthManager(sqliteStore)
	bootstrap, err := authManager.Bootstrap(os.Getenv("DIANA_ADMIN_USERNAME"), os.Getenv("DIANA_ADMIN_PASSWORD"))
	if err != nil {
		log.Fatalf("bootstrap admin credentials: %v", err)
	}
	if bootstrap.Created {
		_, _ = fmt.Fprintf(os.Stderr, "\nDiana administrator credentials (shown once)\n  username: %s\n", bootstrap.Username)
		if bootstrap.GeneratedPassword != "" {
			_, _ = fmt.Fprintf(os.Stderr, "  password: %s\n", bootstrap.GeneratedPassword)
		}
		_, _ = fmt.Fprintln(os.Stderr)
	}
	// 反向 ws 客户端可能只填裸地址（如 ws://host:18080，没有 /onebot/v11/ws
	// 后缀）。带 OneBot 握手特征的升级请求不论路径都直接交给 oneBotServer，
	// 它自带 access token 鉴权；必须放在 WebUI 鉴权中间件之前。
	router.Use(func(c *gin.Context) {
		if assistant.IsOneBotReverseHandshake(c.Request) {
			oneBotServer.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}
		c.Next()
	})
	router.Use(authManager.Middleware())
	authHandler := webui.NewAuthHandler(authManager)
	authHandler.SetLogStore(sqliteStore)
	authHandler.Register(router)
	// ownerLoginHandler 在 botRuntime 创建后注册（见下方）。
	if err := router.SetTrustedProxies(nil); err != nil {
		log.Fatal(err)
	}
	handler.Register(router)
	systemHandler.Register(router)
	botHandler.Register(router)
	ownerLoginHandler := webui.NewOwnerLoginHandler(authManager, botRuntime)
	ownerLoginHandler.SetLogStore(sqliteStore)
	ownerLoginHandler.Register(router)
	botRuntime.SetPrivateMessageInterceptor(ownerLoginHandler.ConsumePrivateMessage)
	napCatLoginHandler.Register(router)
	// 重启复用 SIGTERM 的优雅关停链路：取消根 ctx 让 Serve 返回，再由
	// main 收尾时判断 restartRequested 原地重启。
	var restartRequested atomic.Bool
	restartHandler := webui.NewRestartHandler(func() {
		if restartRequested.CompareAndSwap(false, true) {
			cancel()
		}
	})
	restartHandler.SetLogStore(sqliteStore)
	restartHandler.Register(router)
	logHandler.Register(router)
	statsHandler.Register(router)
	eventStreamHandler.Register(router)
	healthHandler.Register(router)
	// This tokenized endpoint intentionally sits outside /api so a separate
	// NapCat container can fetch media without a WebUI login session.
	router.GET("/media/resolver/:token", func(c *gin.Context) {
		localMediaStore.ServeToken(c.Writer, c.Request, c.Param("token"))
	})
	// Keep historical media URLs valid across upgrades. These token-only routes
	// contain no browseable file path and are intentionally exempt from sessions.
	router.GET("/api/qqbot/media/:token", func(c *gin.Context) {
		localMediaStore.ServeToken(c.Writer, c.Request, c.Param("token"))
	})
	router.GET("/api/assistant/media/:token", func(c *gin.Context) {
		localMediaStore.ServeToken(c.Writer, c.Request, c.Param("token"))
	})
	// OneBot 路由必须在 SPA fallback 之前注册，否则 NapCat 会拿到前端 HTML 而不是 WebSocket。
	router.GET("/onebot/v11/ws", gin.WrapH(oneBotServer))
	router.NoRoute(spaHandler(http.Dir(frontendDistDir())))

	addr := net.JoinHostPort(host, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("webui listening on http://%s:%s", displayHost(host), port)
	server := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			log.Printf("webui shutdown failed: %v", shutdownErr)
		}
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if restartRequested.Load() {
		log.Printf("webui restarting")
		if err := relaunchSelf(closeLog); err != nil {
			log.Fatalf("webui restart failed: %v", err)
		}
	}
}

func newSystemUpdater() (*updater.GitUpdater, error) {
	root := strings.TrimSpace(os.Getenv("DIANA_UPDATE_ROOT"))
	if root == "" {
		root = "."
	}
	runningExecutable, _ := os.Executable()
	runningCommit := strings.TrimSpace(buildVersion)
	if strings.EqualFold(runningCommit, "dev") {
		runningCommit = ""
	}
	options := updater.Options{
		RunningCommit:     runningCommit,
		RunningExecutable: runningExecutable,
	}
	applyScript := filepath.Join(root, "scripts", "apply-update.sh")
	if runtime.GOOS != "windows" && boolFromEnv("DIANA_UPDATE_APPLY_ENABLED", true) {
		if info, statErr := os.Stat(applyScript); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			options.ApplyCommand = []string{applyScript}
		}
	}
	return updater.NewGitUpdaterWithOptions(root, options)
}

func probeMacOSQQAppDataAccess() {
	if runtime.GOOS != "darwin" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("macOS QQ app data access probe skipped: %v", err)
		return
	}
	path := filepath.Join(home, "Library", "Containers", "com.tencent.qq", "Data", ".config", "QQ", "NapCat", "temp")
	if _, err := os.ReadDir(path); err != nil {
		log.Printf("macOS QQ app data access denied: %v", err)
		return
	}
	log.Printf("macOS QQ app data access granted")
}

// migrateLegacyLLMConfigPluginState 把旧全局插件开关迁到每个机器人，并从插件状态中移除旧条目。
func migrateLegacyLLMConfigPluginState(set assistant.ProfileSet, states map[string]assistant.PluginState) (assistant.ProfileSet, bool) {
	legacy, ok := states[legacyLLMConfigPluginID]
	if !ok {
		return set, false
	}
	delete(states, legacyLLMConfigPluginID)
	if legacy.Enabled {
		return set, false
	}

	disabled := false
	changed := false
	for i := range set.Profiles {
		if set.Profiles[i].OwnerLLMConfigEnabled != nil {
			continue
		}
		set.Profiles[i].OwnerLLMConfigEnabled = &disabled
		changed = true
	}
	return set, changed
}

// migrateRestoredWebSearchPluginState upgrades the former optional search plugin
// to a built-in capability. An explicitly disabled installed plugin stays
// disabled, while the old "not installed" state becomes installed and enabled.
func migrateRestoredWebSearchPluginState(states map[string]assistant.PluginState, catalogState assistant.PluginState) bool {
	state, exists := states[webSearchPluginID]
	if !exists {
		catalogState.Installed = true
		catalogState.Enabled = true
		states[webSearchPluginID] = catalogState
		return true
	}
	if state.Manifest.BuiltIn && state.Installed {
		return false
	}
	wasInstalled := state.Installed
	state.Manifest = catalogState.Manifest
	state.Installed = true
	if !wasInstalled {
		state.Enabled = true
	}
	states[webSearchPluginID] = state
	return true
}

func displayHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return host
	}
}

func limitRequestBody(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || maxBytes <= 0 {
			c.Next()
			return
		}
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// setupLogging 配置控制台和文件日志输出。
func setupLogging() (io.Writer, func()) {
	logPath := envOrAny([]string{"LOG_PATH", "DIANA_LOG_PATH"}, "")
	if logPath == "" {
		return os.Stdout, func() {}
	}
	// Gin 请求日志和标准 log 共用同一个 writer，方便部署时只收集一个文件。
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		log.Printf("create log directory skipped: %v", err)
		return os.Stdout, func() {}
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("open log file skipped: %v", err)
		return os.Stdout, func() {}
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		log.Printf("secure log file skipped: %v", err)
		return os.Stdout, func() {}
	}
	writer := io.MultiWriter(os.Stdout, file)
	log.SetOutput(writer)
	log.Printf("logging to %s", logPath)
	return writer, func() {
		_ = file.Close()
	}
}

// qqBotConfigFromEnv 从环境变量构建 QQ 机器人默认配置。
func qqBotConfigFromEnv() assistant.BotConfig {
	cfg := assistant.DefaultBotConfig()
	// 默认回连到当前 WebUI 端口，开发环境只要 NapCat 指向这个地址即可联调。
	defaultOneBotEndpoint := "ws://127.0.0.1:" + envOr("PORT", "18080") + "/onebot/v11/ws"
	cfg.Enabled = boolFromEnv("QQBOT_ENABLED", cfg.Enabled)
	cfg.OneBotReverseWSEndpoint = envOrAny([]string{"ONEBOT_REVERSE_WS_ENDPOINT", "QQBOT_ONEBOT_REVERSE_WS_ENDPOINT"}, defaultOneBotEndpoint)
	cfg.OneBotAccessToken = envOrAny([]string{"ONEBOT_ACCESS_TOKEN", "QQBOT_ONEBOT_ACCESS_TOKEN"}, "")
	cfg.NoneBotBridgeEnabled = boolFromEnv("NONEBOT_BRIDGE_ENABLED", cfg.NoneBotBridgeEnabled)
	cfg.NoneBotBridgeEndpoint = envOrAny([]string{"NONEBOT_BRIDGE_ENDPOINT", "QQBOT_NONEBOT_BRIDGE_ENDPOINT"}, cfg.NoneBotBridgeEndpoint)
	cfg.NoneBotBridgeToken = envOrAny([]string{"NONEBOT_BRIDGE_TOKEN", "QQBOT_NONEBOT_BRIDGE_TOKEN"}, "")
	cfg.BotQQ = envOrAny([]string{"QQBOT_SELF_ID", "QQBOT_QQ", "BOT_QQ"}, "")
	cfg.OwnerID = envOrAny([]string{"DIANA_OWNER_ID", "QQBOT_OWNER_ID"}, "")
	cfg.GroupTriggers = stringListFromEnv("DIANA_GROUP_TRIGGERS", cfg.GroupTriggers)
	cfg.SystemPrompt = envOrAny([]string{"DIANA_SYSTEM_PROMPT", "QQBOT_SYSTEM_PROMPT"}, cfg.SystemPrompt)
	cfg.ProactiveReplyRouterPrompt = envOrAny([]string{"DIANA_PROACTIVE_REPLY_ROUTER_PROMPT", "DIANA_PASSIVE_REPLY_ROUTER_PROMPT"}, cfg.ProactiveReplyRouterPrompt)
	cfg.ProactiveReplyPrompt = envOrAny([]string{"DIANA_PROACTIVE_REPLY_PROMPT", "DIANA_PASSIVE_REPLY_PROMPT"}, cfg.ProactiveReplyPrompt)
	cfg.ErrorReplyPrefix = envOr("DIANA_ERROR_REPLY_PREFIX", cfg.ErrorReplyPrefix)
	cfg.SendRetryAttempts = intFromEnv("DIANA_SEND_RETRY_ATTEMPTS", cfg.SendRetryAttempts)
	cfg.SendChunkIntervalMS = intFromEnv("DIANA_SEND_CHUNK_INTERVAL_MS", cfg.SendChunkIntervalMS)
	cfg.MaxInputChars = intFromEnv("DIANA_MAX_INPUT_CHARS", cfg.MaxInputChars)
	cfg.MaxReplyChars = intFromEnv("DIANA_MAX_REPLY_CHARS", cfg.MaxReplyChars)
	cfg.DirectReplyChunkSize = intFromEnv("DIANA_DIRECT_REPLY_CHUNK_SIZE", cfg.DirectReplyChunkSize)
	cfg.ForwardReplyThreshold = intFromEnv("DIANA_FORWARD_REPLY_THRESHOLD", cfg.ForwardReplyThreshold)
	cfg.RecallReplyMode = assistant.RecallReplyMode(envOr("DIANA_RECALL_REPLY_MODE", string(cfg.RecallReplyMode)))
	llmQQIDMaskingEnabled := boolFromEnv("DIANA_LLM_QQ_ID_MASKING_ENABLED", true)
	cfg.LLMQQIDMaskingEnabled = &llmQQIDMaskingEnabled
	cfg.RecentContextLimit = intFromEnv("DIANA_RECENT_GROUP_CONTEXT_LIMIT", cfg.RecentContextLimit)
	cfg.ContextSummaryThreshold = intFromEnv("DIANA_CONTEXT_SUMMARY_THRESHOLD", cfg.ContextSummaryThreshold)
	if chance, ok := floatFromEnvAny("DIANA_PROACTIVE_REPLY_CHANCE", "DIANA_PASSIVE_REPLY_CHANCE"); ok {
		cfg.ProactiveReplyChance = chance
	}
	if threshold, ok := floatFromEnv("DIANA_PROACTIVE_REPLY_THRESHOLD"); ok {
		cfg.ProactiveReplyThreshold = threshold
	} else if threshold, ok := floatFromEnv("DIANA_PASSIVE_REPLY_THRESHOLD"); ok {
		if threshold == 0.8 {
			threshold = 0.9
		}
		cfg.ProactiveReplyThreshold = threshold
	}
	cfg.MaxBotConcurrency = intFromEnv("DIANA_MAX_BOT_CONCURRENCY", cfg.MaxBotConcurrency)
	cfg.RequestTimeout = time.Duration(int64FromEnv("DIANA_HTTP_TIMEOUT_SECONDS", int64(cfg.RequestTimeout.Seconds()))) * time.Second
	cfg.AgentEnabled = boolFromEnv("DIANA_AGENT_ENABLED", cfg.AgentEnabled)
	cfg.AgentWorkDir = envOrAny([]string{"DIANA_AGENT_WORK_DIR", "AGENT_WORK_DIR"}, cfg.AgentWorkDir)
	cfg.AgentMaxSteps = intFromEnv("DIANA_AGENT_MAX_STEPS", cfg.AgentMaxSteps)
	cfg.AgentSkillRoots = stringListFromEnv("DIANA_AGENT_SKILL_ROOTS", cfg.AgentSkillRoots)
	cfg.AgentMCPConfigPath = envOrAny([]string{"DIANA_AGENT_MCP_CONFIG", "AGENT_MCP_CONFIG"}, cfg.AgentMCPConfigPath)
	cfg.AgentCommandAllowlist = stringListFromEnv("DIANA_AGENT_COMMAND_ALLOWLIST", cfg.AgentCommandAllowlist)
	cfg.AgentCommandTimeoutMS = intFromEnv("DIANA_AGENT_COMMAND_TIMEOUT_MS", cfg.AgentCommandTimeoutMS)
	cfg.AgentBrowserCDPURL = envOrAny([]string{"DIANA_AGENT_BROWSER_CDP_URL", "AGENT_BROWSER_CDP_URL"}, cfg.AgentBrowserCDPURL)
	cfg.AgentBrowserTimeoutMS = intFromEnv("DIANA_AGENT_BROWSER_TIMEOUT_MS", cfg.AgentBrowserTimeoutMS)
	return cfg.WithDefaults()
}

// llmConfigFromEnv 从环境变量构建默认 LLM provider 配置。
func llmConfigFromEnv() llm.ProviderConfig {
	provider := providerFromEnv("LLM_PROVIDER", llm.ProviderOpenAICompatible)
	cfg := llm.ProviderConfig{
		Provider:            provider,
		APIKey:              os.Getenv("LLM_API_KEY"),
		BaseURL:             os.Getenv("LLM_BASE_URL"),
		APIFormat:           llm.APIFormat(os.Getenv("LLM_API_FORMAT")),
		Model:               envOr("LLM_MODEL", llm.DefaultModel(provider)),
		ImageModel:          os.Getenv("LLM_IMAGE_MODEL"),
		ImageBaseURL:        os.Getenv("LLM_IMAGE_BASE_URL"),
		ImageOrigin:         os.Getenv("LLM_IMAGE_ORIGIN"),
		ImageTimeout:        time.Duration(int64FromEnv("LLM_IMAGE_TIMEOUT_MS", 0)) * time.Millisecond,
		UserAgent:           os.Getenv("LLM_USER_AGENT"),
		ReasoningEffort:     os.Getenv("LLM_REASONING_EFFORT"),
		ContextWindowTokens: int64FromEnv("LLM_CONTEXT_WINDOW_TOKENS", llm.DefaultContextWindowTokens),
		MaxContextTokens:    int64FromEnv("LLM_MAX_CONTEXT_TOKENS", llm.DefaultMaxContextTokens),
		MaxOutputTokens:     int64FromEnv("LLM_MAX_OUTPUT_TOKENS", 1024),
		Timeout:             time.Duration(int64FromEnv("LLM_TIMEOUT_MS", 60000)) * time.Millisecond,
	}
	if temp, ok := floatFromEnv("LLM_TEMPERATURE"); ok {
		cfg.Temperature = &temp
	}
	return cfg.WithDefaults()
}

// frontendDistDir 查找生产前端静态文件目录。
func frontendDistDir() string {
	// 同时兼容源码目录运行、打包后从二进制旁边运行、以及测试工作目录切换。
	// 旧版 frontend 已停用，不再静默回退，避免误把过时控制台部署到生产环境。
	candidates := []string{
		"frontend-next/dist",
		"../../frontend-next/dist",
	}
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		candidates = append([]string{
			filepath.Clean(filepath.Join(executableDir, "..", "Resources", "frontend-next", "dist")),
		}, candidates...)
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(configDir, "diana", "frontend-next", "dist"))
	}
	if custom := envOr("FRONTEND_DIST", ""); custom != "" {
		// 显式指定的目录永远最优先，即使暂时不存在也按它返回，方便部署脚本预创建。
		candidates = append([]string{custom}, candidates...)
	}
	for _, candidate := range candidates {
		if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

// spaHandler 返回前端单页应用的兜底路由处理器。
func spaHandler(root http.FileSystem) gin.HandlerFunc {
	return func(c *gin.Context) {
		// API 未命中时返回 JSON 404，避免前端路由兜底掩盖接口拼写错误。
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		file, err := root.Open(path)
		if err != nil {
			serveFile(c, root, "index.html")
			return
		}
		_ = file.Close()
		serveFile(c, root, path)
	}
}

// serveFile 按静态文件路径写出响应内容。
func serveFile(c *gin.Context, root http.FileSystem, path string) {
	file, err := root.Open(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	defer file.Close()

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if stat, statErr := file.Stat(); statErr == nil {
		etag := `"` + strconv.FormatInt(stat.Size(), 16) + "-" + strconv.FormatInt(stat.ModTime().UnixNano(), 16) + `"`
		c.Header("ETag", etag)
		if c.GetHeader("If-None-Match") == etag {
			c.Status(http.StatusNotModified)
			return
		}
	}
	if filepath.Base(path) == "index.html" {
		// SPA 入口只做 ETag 条件校验；未变化返回 304，新构建才重新下载。
		c.Header("Cache-Control", "no-cache, must-revalidate")
	} else if strings.HasPrefix(filepath.ToSlash(path), "assets/") {
		// Vite 产物文件名带内容哈希，可以长期缓存；新构建会生成新 URL。
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "no-cache")
	}
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, file); err != nil {
		log.Printf("serve %s: %v", path, err)
	}
}

// envOr 读取环境变量，空值时返回默认值。
// envInt 读取正整数环境变量，未设置或非法时返回 fallback。
func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// envOrAny 按顺序读取多个环境变量并返回第一个非空值。
func envOrAny(keys []string, fallback string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return fallback
}

// boolFromEnv 将环境变量解析为布尔值。
func boolFromEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// intFromEnv 将环境变量解析为 int。
func intFromEnv(key string, fallback int) int {
	parsed := int64FromEnv(key, int64(fallback))
	if parsed > int64(^uint(0)>>1) {
		return fallback
	}
	return int(parsed)
}

// stringListFromEnv 将逗号分隔的环境变量解析为字符串列表。
func stringListFromEnv(key string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// providerFromEnv 将环境变量解析为受支持的 LLM provider。
func providerFromEnv(key string, fallback llm.Provider) llm.Provider {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case string(llm.ProviderGemini), "google", "google_genai":
		return llm.ProviderGemini
	case string(llm.ProviderAnthropic), "claude":
		return llm.ProviderAnthropic
	case string(llm.ProviderOpenAICompatible), "openai", "openai-compatible":
		return llm.ProviderOpenAICompatible
	default:
		return fallback
	}
}

// int64FromEnv 将环境变量解析为 int64。
func int64FromEnv(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// floatFromEnv 将环境变量解析为 float64，并返回是否解析成功。
func floatFromEnv(key string) (float64, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func floatFromEnvAny(keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := floatFromEnv(key); ok {
			return value, true
		}
	}
	return 0, false
}
