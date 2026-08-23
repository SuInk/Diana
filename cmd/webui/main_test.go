// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

func TestSPAHandlerCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := http.FS(fstest.MapFS{
		"index.html":          {Data: []byte("<html></html>")},
		"assets/index-abc.js": {Data: []byte("console.log('ok')")},
	})
	router := gin.New()
	router.NoRoute(spaHandler(root))

	tests := []struct {
		path      string
		wantCache string
	}{
		{path: "/", wantCache: "no-cache, must-revalidate"},
		{path: "/settings", wantCache: "no-cache, must-revalidate"},
		{path: "/assets/index-abc.js", wantCache: "public, max-age=31536000, immutable"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
		})
	}
}

func TestSPAHandlerReturnsNotModifiedForMatchingETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := http.FS(fstest.MapFS{
		"index.html": {Data: []byte("<html></html>")},
	})
	router := gin.New()
	router.NoRoute(spaHandler(root))

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is empty")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	router.ServeHTTP(second, req)
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", second.Code, http.StatusNotModified)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0", second.Body.Len())
	}
}

func TestLimitRequestBodyRejectsOversizedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	router := gin.New()
	router.Use(limitRequestBody(4))
	router.POST("/api/test", func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader("12345"))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("status = %d, called = %v", recorder.Code, called)
	}
}

func TestMigrateLegacyLLMConfigPluginState(t *testing.T) {
	explicitlyEnabled := true
	set := assistant.ProfileSet{
		ActiveID: "primary",
		Profiles: []assistant.BotConfig{
			{ID: "primary", Name: "Primary"},
			{ID: "secondary", Name: "Secondary", OwnerLLMConfigEnabled: &explicitlyEnabled},
		},
	}
	states := map[string]assistant.PluginState{
		legacyLLMConfigPluginID: {Enabled: false},
		"official.file-parser-go": {
			Enabled: true,
		},
	}

	migrated, changed := migrateLegacyLLMConfigPluginState(set, states)
	if !changed {
		t.Fatal("expected profile migration")
	}
	if _, exists := states[legacyLLMConfigPluginID]; exists {
		t.Fatal("legacy plugin state was not removed")
	}
	if _, exists := states["official.file-parser-go"]; !exists {
		t.Fatal("unrelated plugin state was removed")
	}
	if migrated.Profiles[0].OwnerLLMConfigEnabled == nil || *migrated.Profiles[0].OwnerLLMConfigEnabled {
		t.Fatalf("primary setting = %#v, want false", migrated.Profiles[0].OwnerLLMConfigEnabled)
	}
	if migrated.Profiles[1].OwnerLLMConfigEnabled == nil || !*migrated.Profiles[1].OwnerLLMConfigEnabled {
		t.Fatalf("explicit setting = %#v, want preserved true", migrated.Profiles[1].OwnerLLMConfigEnabled)
	}
}

func TestMigrateRestoredWebSearchPluginState(t *testing.T) {
	states := map[string]assistant.PluginState{
		"official.file-parser-go": {Installed: true, Enabled: true},
	}
	catalog := assistant.PluginState{
		Manifest:  assistant.PluginManifest{ID: webSearchPluginID, Name: "联网搜索", BuiltIn: true},
		Installed: true,
		Enabled:   true,
	}
	if !migrateRestoredWebSearchPluginState(states, catalog) {
		t.Fatal("expected legacy installation to gain the restored search plugin")
	}
	got := states[webSearchPluginID]
	if !got.Installed || !got.Enabled || got.Manifest.ID != webSearchPluginID {
		t.Fatalf("migrated state = %#v", got)
	}

	states[webSearchPluginID] = assistant.PluginState{
		Manifest: assistant.PluginManifest{ID: webSearchPluginID, BuiltIn: false},
		Settings: map[string]any{"max_results": float64(7)},
	}
	if !migrateRestoredWebSearchPluginState(states, catalog) {
		t.Fatal("expected old uninstalled search state to be upgraded")
	}
	got = states[webSearchPluginID]
	if !got.Installed || !got.Enabled || !got.Manifest.BuiltIn || got.Settings["max_results"] != float64(7) {
		t.Fatalf("upgraded uninstalled state = %#v", got)
	}

	states[webSearchPluginID] = assistant.PluginState{
		Manifest:  assistant.PluginManifest{ID: webSearchPluginID, BuiltIn: false},
		Installed: true,
		Enabled:   false,
	}
	if !migrateRestoredWebSearchPluginState(states, catalog) {
		t.Fatal("expected old installed search state to gain built-in manifest")
	}
	got = states[webSearchPluginID]
	if !got.Installed || got.Enabled || !got.Manifest.BuiltIn {
		t.Fatalf("upgraded disabled state = %#v", got)
	}

	if migrateRestoredWebSearchPluginState(states, catalog) {
		t.Fatal("current built-in search state must be preserved")
	}
	if states[webSearchPluginID].Enabled {
		t.Fatal("explicitly disabled search plugin was re-enabled")
	}
}

// TestBotChannelSetFactoryBindsListenerToEnabledOneBotProfile 固定共享监听器的
// token 来源：只认启用中的 OneBot 配置档，没有就清空，绝不留着上一次的 token。
func TestBotChannelSetFactoryBindsListenerToEnabledOneBotProfile(t *testing.T) {
	const token = "0123456789abcdef"
	server := assistant.NewOneBotReverseServer(assistant.OneBotConfig{})
	factory := newBotChannelSetFactory(server)

	oneBot := assistant.DefaultBotConfig()
	oneBot.ID = "onebot"
	oneBot.Platform = assistant.PlatformOneBotV11
	oneBot.Enabled = true
	oneBot.OneBotReverseWSEndpoint = "ws://127.0.0.1:18080/onebot/v11/ws"
	oneBot.OneBotAccessToken = token

	telegram := assistant.DefaultBotConfig()
	telegram.ID = "telegram"
	telegram.Platform = assistant.PlatformTelegram
	telegram.Enabled = true
	telegram.TelegramBotToken = "telegram-token"

	// 激活档是 Telegram，OneBot 档只是启用着：监听器仍必须绑到 OneBot 档的 token。
	set := assistant.ProfileSet{ActiveID: telegram.ID, Profiles: []assistant.BotConfig{telegram, oneBot}}
	factory(set)
	request := httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusUnauthorized {
		t.Fatal("listener rejected the token of the enabled OneBot profile")
	}
	if !server.Status().AccessTokenConfigured {
		t.Fatal("status must report the listener holds a token")
	}

	// OneBot 档被停用后，监听器不能继续拿着旧 token 收连接。
	oneBot.Enabled = false
	factory(assistant.ProfileSet{ActiveID: telegram.ID, Profiles: []assistant.BotConfig{telegram, oneBot}})
	if server.Status().AccessTokenConfigured {
		t.Fatal("disabled OneBot profile must clear the listener token")
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest("GET", "http://localhost/onebot/v11/ws", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 after the profile was disabled", recorder.Code)
	}
	if event := server.Status().LastConnectionEvent; event != "unauthorized:server_token_unset" {
		t.Fatalf("last connection event = %q", event)
	}
}

// TestLoadAppConfigReadsBothLayers 覆盖配置文件的两层：基础设施段每次启动都读，
// 业务段按 WebUI 接口的 payload 字段名解析。
func TestLoadAppConfigReadsBothLayers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
server:
  host: 0.0.0.0
  port: "19000"
  trusted_proxies:
    - 10.0.0.1
    - "  "
storage:
  db_path: /data/diana.db
  media_max_mb: 25
admin:
  username: owner
update:
  apply_enabled: false
bot:
  platform: onebot-v11
  enabled: true
  onebot_access_token: "0123456789abcdef"
  owner_id: "10001"
  group_triggers: [Diana, diana]
llm:
  provider: openai_compatible
  base_url: https://api.example.com/v1
  api_key: seed-key
  model: gp5.5
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAppConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Host != "0.0.0.0" || cfg.Server.Port != "19000" || cfg.Storage.DBPath != "/data/diana.db" || cfg.Storage.MediaMaxMB != 25 || cfg.Admin.Username != "owner" {
		t.Fatalf("infrastructure layer = %+v %+v %+v", cfg.Server, cfg.Storage, cfg.Admin)
	}
	if got := trimmedList(cfg.Server.TrustedProxies); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Fatalf("trusted proxies = %v", got)
	}
	// 没写的布尔项保持默认值，写了的按写的来——两者必须区分开，否则「没配置」
	// 会被当成「配置为 false」。
	if boolOr(cfg.Update.ApplyEnabled, true) || !boolOr(cfg.Update.ReleaseEnabled, true) {
		t.Fatalf("update flags = %v / %v", cfg.Update.ApplyEnabled, cfg.Update.ReleaseEnabled)
	}

	bot, seeded, err := cfg.botSeedConfig(defaultOneBotEndpoint("19000"))
	if err != nil {
		t.Fatal(err)
	}
	if !seeded || bot.OneBotAccessToken != "0123456789abcdef" || bot.OwnerID != "10001" || !bot.Enabled {
		t.Fatalf("bot seed = %+v seeded=%v", bot, seeded)
	}
	// 配置文件没写反连地址时按实际端口兜底，不能退回写死的 18080。
	if bot.OneBotReverseWSEndpoint != "ws://127.0.0.1:19000/onebot/v11/ws" {
		t.Fatalf("bot endpoint = %q", bot.OneBotReverseWSEndpoint)
	}
	if len(bot.GroupTriggers) != 2 {
		t.Fatalf("group triggers = %v", bot.GroupTriggers)
	}

	provider, seeded, err := cfg.llmSeedConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !seeded || provider.APIKey != "seed-key" || provider.Model != "gp5.5" || provider.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("llm seed = %+v seeded=%v", provider, seeded)
	}
}

// TestLoadAppConfigTreatsMissingFileAsDefaults 全新部署第一次跑起来就该能进
// 安装向导，而不是先要求写一份 YAML。
func TestLoadAppConfigTreatsMissingFileAsDefaults(t *testing.T) {
	cfg, err := loadAppConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing config must not fail: %v", err)
	}
	if cfg.path != "" {
		t.Fatalf("path = %q, want empty so startup logs say defaults are in use", cfg.path)
	}
	if _, seeded, err := cfg.botSeedConfig(defaultOneBotEndpoint("18080")); err != nil || seeded {
		t.Fatalf("bot seeded = %v, err = %v", seeded, err)
	}
	if _, seeded, err := cfg.llmSeedConfig(); err != nil || seeded {
		t.Fatalf("llm seeded = %v, err = %v", seeded, err)
	}
}

// TestLoadAppConfigRejectsBrokenYAML 配置写错必须直接报错退出，不能静默用默认值
// 顶上去——那样等于把一份没生效的配置留在机器上继续误导人。
func TestLoadAppConfigRejectsBrokenYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: \"18080\"\n bad indent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAppConfig(path); err == nil {
		t.Fatal("broken YAML must fail loudly")
	}
}

// TestResolveConfigPathPrefersExplicitFlag 固定查找顺序：--config 高于
// DIANA_CONFIG，两者都没有才看约定位置。
func TestResolveConfigPathPrefersExplicitFlag(t *testing.T) {
	t.Setenv(configPathEnv, "/from/env.yaml")
	if got := resolveConfigPath([]string{"--config", "/from/flag.yaml"}); got != "/from/flag.yaml" {
		t.Fatalf("path = %q", got)
	}
	if got := resolveConfigPath([]string{"--config=/from/equals.yaml"}); got != "/from/equals.yaml" {
		t.Fatalf("path = %q", got)
	}
	if got := resolveConfigPath(nil); got != "/from/env.yaml" {
		t.Fatalf("path = %q", got)
	}
}
