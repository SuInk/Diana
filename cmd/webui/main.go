package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
// buildVersion 由构建时 -ldflags "-X main.buildVersion=<sha>" 注入；
// 源码运行或未注入时展示 dev，git 可用时前端优先展示 git 提交号。
var buildVersion = "dev"

func main() {
	logWriter, closeLog := setupLogging()
	defer closeLog()
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
	// 模型列表必须从当前 provider 后端读取，QQ 聊天技能和 WebUI 共用同一套校验逻辑。
	modelListFactory := func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return llm.ListModels(ctx, cfg)
	}
	handler := webui.NewLLMConfigHandler(store)
	handler.SetModelListFactory(modelListFactory)
	handler.SetLogStore(sqliteStore)
	systemUpdater, err := updater.NewGitUpdater(".")
	if err != nil {
		log.Fatal(err)
	}
	systemHandler := webui.NewSystemUpdateHandler(systemUpdater)
	systemHandler.SetLogStore(sqliteStore)
	systemHandler.SetBuildVersion(buildVersion)
	// 自动更新循环：按持久化设置周期性 fetch + ff-only pull，重启后生效的提示由前端负责。
	autoUpdater := webui.NewAutoUpdater(systemUpdater, sqliteStore, sqliteStore)
	systemHandler.SetAutoUpdater(autoUpdater)
	go autoUpdater.Run(ctx)
	runtimePersistor := webui.NewRuntimePersistor(botProfileStore)
	botCfg := botProfileStore.Current()
	plugins := assistant.NewDefaultPluginManager()
	if savedPluginStates, ok, err := sqliteStore.LoadPluginStates(ctx); err != nil {
		log.Fatal(err)
	} else if ok {
		plugins.Restore(savedPluginStates)
	}
	// NapCat 使用反向 WebSocket 连接本服务；这里保留同一个 server 实例，配置变更时只更新 token/endpoint。
	oneBotServer := assistant.NewOneBotReverseServer(assistant.OneBotConfig{
		Endpoint:    botCfg.OneBotReverseWSEndpoint,
		AccessToken: botCfg.OneBotAccessToken,
	})
	botRuntime := assistant.NewRuntime(botCfg, oneBotServer, plugins, store, reminderStore, runtimePersistor, func() (assistant.LLMProvider, error) {
		return llm.NewClient(store.Current())
	})
	botRuntime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (assistant.LLMProvider, error) {
		return llm.NewClient(cfg)
	})
	botRuntime.SetGroupConfigStore(botGroupConfigStore)
	botRuntime.SetLLMModelLister(modelListFactory)
	botRuntime.SetAppLogWriter(sqliteStore)
	localMediaBaseURL := envOr(
		"DIANA_LOCAL_MEDIA_BASE_URL",
		"http://"+net.JoinHostPort(displayHost(host), port)+"/media/resolver",
	)
	localMediaStore := assistant.NewLocalMediaStore(localMediaBaseURL)
	botRuntime.SetLocalMediaSharer(localMediaStore)
	// 统计和 SSE 推送共用同一个事件监听器，Dashboard 依赖这两条链路。
	statsCollector := webui.NewStatsCollector()
	eventHub := webui.NewEventHub()
	botRuntime.SetEventListener(func(event assistant.EventRecord) {
		statsCollector.Observe(event)
		eventHub.PublishBotEvent(event)
	})
	if botCfg.Enabled {
		if err := botRuntime.Start(ctx); err != nil {
			log.Printf("assistant start skipped: %v", err)
		}
	}
	botHandler := webui.NewQQBotHandlerWithFactory(ctx, botRuntime, func(cfg assistant.BotConfig) assistant.Channel {
		oneBotServer.SetConfig(assistant.OneBotConfig{
			Endpoint:    cfg.OneBotReverseWSEndpoint,
			AccessToken: cfg.OneBotAccessToken,
		})
		return oneBotServer
	})
	botHandler.SetFeatureFlags(webui.QQBotFeatureFlags{
		GroupTest: boolFromEnv("QQBOT_GROUP_TEST_ENABLED", false),
	})
	botHandler.SetProfileStore(botProfileStore)
	botHandler.SetGroupConfigStore(botGroupConfigStore)
	botHandler.SetSQLiteStore(sqliteStore)
	logHandler := webui.NewAppLogHandler(sqliteStore)
	statsHandler := webui.NewStatsHandler(statsCollector, botRuntime)
	eventStreamHandler := webui.NewEventStreamHandler(eventHub, botRuntime, statsCollector)
	eventStreamHandler.StartWatcher(ctx, 2*time.Second)
	healthHandler := webui.NewHealthHandler()

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.LoggerWithWriter(logWriter), gin.RecoveryWithWriter(logWriter))
	// 鉴权中间件必须在业务路由之前挂载；未设密码时等价于关闭。
	authManager := webui.NewAuthManager(sqliteStore)
	generatedPassword, err := authManager.Bootstrap(os.Getenv("DIANA_ADMIN_PASSWORD"))
	if err != nil {
		log.Fatalf("bootstrap admin password: %v", err)
	}
	if generatedPassword != "" {
		_, _ = fmt.Fprintf(os.Stderr, "\nDiana administrator credentials (shown once)\n  username: admin@diana.local\n  password: %s\n\n", generatedPassword)
	}
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
	logHandler.Register(router)
	statsHandler.Register(router)
	eventStreamHandler.Register(router)
	healthHandler.Register(router)
	// This tokenized endpoint intentionally sits outside /api so a separate
	// NapCat container can fetch media without a WebUI login session.
	router.GET("/media/resolver/:token", func(c *gin.Context) {
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
	if err := router.RunListener(listener); err != nil {
		log.Fatal(err)
	}
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

// setupLogging 配置控制台和文件日志输出。
func setupLogging() (io.Writer, func()) {
	logPath := envOrAny([]string{"LOG_PATH", "DIANA_LOG_PATH"}, "")
	if logPath == "" {
		return os.Stdout, func() {}
	}
	// Gin 请求日志和标准 log 共用同一个 writer，方便部署时只收集一个文件。
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		log.Printf("create log directory skipped: %v", err)
		return os.Stdout, func() {}
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("open log file skipped: %v", err)
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
	cfg.ErrorReplyPrefix = envOr("DIANA_ERROR_REPLY_PREFIX", cfg.ErrorReplyPrefix)
	cfg.SendRetryAttempts = intFromEnv("DIANA_SEND_RETRY_ATTEMPTS", cfg.SendRetryAttempts)
	cfg.SendChunkIntervalMS = intFromEnv("DIANA_SEND_CHUNK_INTERVAL_MS", cfg.SendChunkIntervalMS)
	cfg.MaxInputChars = intFromEnv("DIANA_MAX_INPUT_CHARS", cfg.MaxInputChars)
	cfg.MaxReplyChars = intFromEnv("DIANA_MAX_REPLY_CHARS", cfg.MaxReplyChars)
	cfg.DirectReplyChunkSize = intFromEnv("DIANA_DIRECT_REPLY_CHUNK_SIZE", cfg.DirectReplyChunkSize)
	cfg.ForwardReplyThreshold = intFromEnv("DIANA_FORWARD_REPLY_THRESHOLD", cfg.ForwardReplyThreshold)
	cfg.RecentContextLimit = intFromEnv("DIANA_RECENT_GROUP_CONTEXT_LIMIT", cfg.RecentContextLimit)
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
		Provider:        provider,
		APIKey:          os.Getenv("LLM_API_KEY"),
		BaseURL:         os.Getenv("LLM_BASE_URL"),
		Model:           envOr("LLM_MODEL", llm.DefaultModel(provider)),
		ImageModel:      os.Getenv("LLM_IMAGE_MODEL"),
		UserAgent:       os.Getenv("LLM_USER_AGENT"),
		MaxOutputTokens: int64FromEnv("LLM_MAX_OUTPUT_TOKENS", 1024),
		Timeout:         time.Duration(int64FromEnv("LLM_TIMEOUT_MS", 30000)) * time.Millisecond,
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
