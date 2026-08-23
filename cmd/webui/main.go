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
	"github.com/SuInk/diana/model/version"
	"github.com/SuInk/diana/webui"

	"github.com/gin-gonic/gin"
)

// main 初始化存储、路由、机器人运行时并启动 WebUI 服务。
// buildVersion 由构建时 -ldflags "-X main.buildVersion=<version>" 注入；
// Release 使用语义化 tag，普通开发构建使用 dev。
var buildVersion = "dev"

// runtimeVersion 是对外展示、并参与 Release 更新比较的版本号。
// 没有注入正式 tag 时回落到源码基线（如 v0.8.36-dev），
// 保证本地构建也能和最新 Release 做语义化比较。
var runtimeVersion = version.Resolve(buildVersion)

const (
	webSearchPluginID = "official.web-search"
)

const maxHTTPRequestBodyBytes = 8 << 20

// newBotChannelSetFactory 按配置集重建全部通道。OneBot 反连监听器是进程内共享
// 的单个实例，它的 endpoint/token 只能由这里决定——这是唯一的写入点，别处再写
// 就会出现运行态和存储配置对不上的 401。
func newBotChannelSetFactory(oneBotServer *assistant.OneBotReverseServer) func(assistant.ProfileSet) assistant.Channel {
	return func(set assistant.ProfileSet) assistant.Channel {
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
				// The reverse WebSocket endpoint is process-wide. OneBot v11 and Telegram can
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
		if !oneBotAdded {
			// 没有启用中的 OneBot 配置档时要显式清空监听器,否则它会一直拿着上一
			// 次的 token 收连接。清空后握手会以 server_token_unset 被拒,状态和
			// 日志里看得见原因,而不是一个对不上任何配置的神秘 401。
			oneBotServer.SetConfig(assistant.OneBotConfig{})
		}
		return assistant.NewMultiChannel(bindings, set.PlatformContextsIsolated())
	}
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == updater.InternalReleaseApplyCommand {
		if err := updater.RunReleaseApplyHelper(os.Args[2]); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "release update failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	appCfg, err := loadAppConfig(resolveConfigPath(os.Args[1:]))
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	logWriter, closeLog := setupLogging(appCfg.Storage.LogPath)
	defer closeLog()
	if appCfg.path != "" {
		log.Printf("config loaded from %s", appCfg.path)
	} else {
		log.Printf("no config file found; using built-in defaults (set %s or pass --config)", configPathEnv)
	}
	probeMacOSClientAppDataAccess()
	port := stringOr(appCfg.Server.Port, "18080")
	host := strings.TrimSpace(appCfg.Server.Host)

	// 所有后台 goroutine 共用这个根 context，收到 Ctrl+C 或 SIGTERM 时统一退出。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	llmSeed, llmSeeded, err := appCfg.llmSeedConfig()
	if err != nil {
		log.Fatal(err)
	}

	// SQLite 同时保存模型、助手、插件、提醒和操作日志配置。
	sqliteStore, err := storage.NewSQLiteStore(strings.TrimSpace(appCfg.Storage.DBPath))
	if err != nil {
		log.Fatal(err)
	}
	if sqliteStore.Path() != "" {
		// 这不是配置入口，是往进程内部传值：model 层几个模块（如历史媒体目录）
		// 按数据库位置决定自己的落盘路径，通过环境变量拿到解析后的绝对路径。
		_ = os.Setenv("APP_DB_PATH", sqliteStore.Path())
	}
	defer func() {
		_ = sqliteStore.Close()
	}()

	store, err := webui.NewPersistentLLMProfileStore(ctx, sqliteStore, llmSeed)
	if err != nil {
		log.Fatal(err)
	}
	botSeed, botSeeded, err := appCfg.botSeedConfig(defaultOneBotEndpoint(port))
	if err != nil {
		log.Fatal(err)
	}
	botProfileStore, err := webui.NewPersistentBotProfileStore(ctx, sqliteStore, botSeed)
	if err != nil {
		log.Fatal(err)
	}
	// 业务配置的真相源是数据库。config.yaml 里写了但没被采用时必须说清楚，
	// 否则就退回到以前那种「改了配置文件重启没反应也不报错」的状态。
	reportSeedOutcome(appCfg.path, llmSeeded, llmSeed, store.Current(), botSeeded, botSeed, botProfileStore.Current())
	botGroupConfigStore, err := webui.NewPersistentBotGroupConfigStore(ctx, sqliteStore)
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
	systemUpdater, err := newSystemUpdater(appCfg.Update)
	if err != nil {
		log.Fatal(err)
	}
	systemHandler := webui.NewSystemUpdateHandler(systemUpdater)
	systemHandler.SetLogStore(sqliteStore)
	systemHandler.SetBuildVersion(runtimeVersion)
	systemHandler.SetBuildType(version.BuildType(buildVersion))
	if err := systemHandler.SetUpdatePolicyStore(ctx, sqliteStore); err != nil {
		log.Fatal(err)
	}
	if err := systemHandler.SetReleaseCacheStore(ctx, sqliteStore); err != nil {
		log.Printf("load system release cache: %v", err)
	}
	releaseUpdater, err := updater.NewReleasePackageUpdater(updater.ReleasePackageOptions{
		CurrentVersion: runtimeVersion,
		FrontendDir:    frontendDistDir(appCfg.Server.FrontendDist),
		DatabasePath:   sqliteStore.Path(),
		HealthURL:      "http://" + net.JoinHostPort(displayHost(host), port) + "/api/health",
		Arguments:      os.Args[1:],
		Shutdown:       cancel,
		Disable:        !boolOr(appCfg.Update.ReleaseEnabled, true),
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
	channelSetFactory := newBotChannelSetFactory(oneBotServer)
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
	configuredMediaBaseURL := strings.TrimSpace(appCfg.Storage.LocalMediaBaseURL)
	localMediaBaseURL := stringOr(
		configuredMediaBaseURL,
		"http://"+net.JoinHostPort(displayHost(host), port)+"/media/resolver",
	)
	localMediaStore := assistant.NewLocalMediaStore(localMediaBaseURL)
	if configuredMediaBaseURL == "" {
		// 未显式配置媒体基址时，按反向 ws 握手时客户端使用的地址动态拼
		// 媒体 URL：桥在容器或别的机器上时（如 host.docker.internal），
		// 能连上 ws 的地址一定也能回源取媒体，用户只需配置 ws 地址。
		localMediaStore.SetOriginProvider(oneBotServer.ConnectionOrigin)
	}
	botRuntime.SetLocalMediaSharer(localMediaStore)
	// 入站图片下载后持久化，识图一律用本地文件的 base64，不依赖模型服务商
	// 能否访问聊天平台那些短时效地址。
	mediaStore := assistant.NewMediaStore(strings.TrimSpace(appCfg.Storage.MediaDir))
	mediaStore.SetLimits(
		int64(appCfg.Storage.MediaMaxMB)<<20,
		int64(appCfg.Storage.MediaCacheMB)<<20,
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
	botHandler := webui.NewBotHandlerWithFactory(ctx, botRuntime, func(cfg assistant.BotConfig) assistant.Channel {
		if cfg.Platform == assistant.PlatformTelegram {
			return assistant.NewTelegramChannel(assistant.TelegramConfig{
				BotToken:   cfg.TelegramBotToken,
				APIBaseURL: cfg.TelegramAPIBaseURL,
				ProxyURL:   cfg.TelegramProxyURL,
			})
		}
		// 这里必须和 channelSetFactory 用同一个平台判断。以前是「不是 Telegram
		// 就当 OneBot」,平台字段一旦不是已注册的 OneBot 平台,两边判断就分叉:
		// 这条路径拿它的(往往是空的)token 覆盖了共享监听器,配置集那条路径又不
		// 认它、不会把 token 写回去,监听器就此停在一个谁都对不上的 token 上。
		if !assistant.IsOneBotPlatform(cfg.Platform) {
			return nil
		}
		oneBotServer.SetConfig(assistant.OneBotConfig{
			Endpoint:    cfg.OneBotReverseWSEndpoint,
			AccessToken: cfg.OneBotAccessToken,
		})
		return oneBotServer
	})
	botHandler.SetChannelSetFactory(channelSetFactory)
	botHandler.SetFeatureFlags(webui.BotFeatureFlags{
		GroupTest: boolOr(appCfg.Update.GroupTest, false),
	})
	botHandler.SetLocalMediaSharer(localMediaStore)
	botHandler.SetProfileStore(botProfileStore)
	botHandler.SetGroupConfigStore(botGroupConfigStore)
	botHandler.SetSQLiteStore(sqliteStore)
	logHandler := webui.NewAppLogHandler(sqliteStore)
	napCatLoginHandler, err := webui.NewNapCatLoginHandler(webui.NapCatLoginConfig{
		BaseURL: strings.TrimSpace(appCfg.NapCat.WebUIURL),
		Token:   strings.TrimSpace(appCfg.NapCat.WebUIToken),
	})
	if err != nil {
		log.Fatal(err)
	}
	statsHandler := webui.NewStatsHandler(statsCollector, botRuntime, sqliteStore.Path())
	eventStreamHandler := webui.NewEventStreamHandler(eventHub, botRuntime, statsCollector, sqliteStore.Path())
	eventStreamHandler.StartWatcher(ctx, 2*time.Second)
	healthHandler := webui.NewHealthHandlerWithVersion(runtimeVersion)
	// 源码部署用 git remote 覆盖仓库标识，Fork 后后台展示的是自己的仓库地址。
	if status, err := systemUpdater.Status(ctx); err == nil {
		healthHandler.SetRepositoryRemote(status.RemoteURL)
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(limitRequestBody(maxHTTPRequestBodyBytes))
	router.Use(gin.LoggerWithWriter(logWriter), gin.RecoveryWithWriter(logWriter))
	// 鉴权中间件必须在业务路由之前挂载；未设密码时等价于关闭。
	authManager := webui.NewAuthManager(sqliteStore)
	bootstrap, err := authManager.Bootstrap(strings.TrimSpace(appCfg.Admin.Username), appCfg.Admin.Password)
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
	// 默认谁都不信：ClientIP() 直接取 TCP 对端地址，伪造 X-Forwarded-For 无效。
	// 但套上反向代理之后所有请求的对端地址都是代理自己，按来源计数的限流会退化
	// 成全局限流，真管理员会被攻击者的失败次数连坐。部署在反代后面时用
	// DIANA_TRUSTED_PROXIES 声明代理地址（逗号分隔的 IP 或 CIDR），声明之后才
	// 会解析 X-Forwarded-For。
	trustedProxies := trimmedList(appCfg.Server.TrustedProxies)
	if err := router.SetTrustedProxies(trustedProxies); err != nil {
		log.Fatalf("DIANA_TRUSTED_PROXIES 配置无效：%v", err)
	}
	if len(trustedProxies) > 0 {
		log.Printf("已信任反向代理 %s，客户端 IP 取自 X-Forwarded-For", strings.Join(trustedProxies, ", "))
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
	// 这条 token-only 路由不含可浏览的文件路径，有意不走会话鉴权。
	router.GET("/api/assistant/media/:token", func(c *gin.Context) {
		localMediaStore.ServeToken(c.Writer, c.Request, c.Param("token"))
	})
	// OneBot 路由必须在 SPA fallback 之前注册，否则 NapCat 会拿到前端 HTML 而不是 WebSocket。
	router.GET("/onebot/v11/ws", gin.WrapH(oneBotServer))
	router.NoRoute(spaHandler(http.Dir(frontendDistDir(appCfg.Server.FrontendDist))))

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

func newSystemUpdater(cfg updateConfig) (*updater.GitUpdater, error) {
	root := stringOr(cfg.Root, ".")
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
	if runtime.GOOS != "windows" && boolOr(cfg.ApplyEnabled, true) {
		if info, statErr := os.Stat(applyScript); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			options.ApplyCommand = []string{applyScript}
		}
	}
	return updater.NewGitUpdaterWithOptions(root, options)
}

func probeMacOSClientAppDataAccess() {
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
func setupLogging(logPath string) (io.Writer, func()) {
	logPath = strings.TrimSpace(logPath)
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

// frontendDistDir 查找生产前端静态文件目录。
func frontendDistDir(custom string) string {
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
	if custom = strings.TrimSpace(custom); custom != "" {
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
