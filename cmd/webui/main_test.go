package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"

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
		Manifest: assistant.PluginManifest{ID: webSearchPluginID, Name: "联网搜索"},
	}
	if !migrateRestoredWebSearchPluginState(states, catalog) {
		t.Fatal("expected legacy installation to gain the restored search plugin")
	}
	got := states[webSearchPluginID]
	if !got.Installed || !got.Enabled || got.Manifest.ID != webSearchPluginID {
		t.Fatalf("migrated state = %#v", got)
	}

	got.Enabled = false
	states[webSearchPluginID] = got
	if migrateRestoredWebSearchPluginState(states, catalog) {
		t.Fatal("explicit search plugin state must be preserved")
	}
	if states[webSearchPluginID].Enabled {
		t.Fatal("explicitly disabled search plugin was re-enabled")
	}
}

func TestQQBotConfigFromEnvRestoresLegacyBehaviorSettings(t *testing.T) {
	t.Setenv("DIANA_PASSIVE_REPLY_ROUTER_PROMPT", "route carefully")
	t.Setenv("DIANA_PASSIVE_REPLY_PROMPT", "reply naturally")
	t.Setenv("DIANA_RECALL_REPLY_MODE", string(assistant.RecallReplyModeOriginalForward))
	t.Setenv("DIANA_LLM_QQ_ID_MASKING_ENABLED", "false")
	t.Setenv("DIANA_RECENT_GROUP_CONTEXT_LIMIT", "11")
	t.Setenv("DIANA_CONTEXT_SUMMARY_THRESHOLD", "37")
	t.Setenv("DIANA_PASSIVE_REPLY_CHANCE", "0.42")
	t.Setenv("DIANA_PASSIVE_REPLY_THRESHOLD", "0.73")

	cfg := qqBotConfigFromEnv()
	if cfg.PassiveReplyRouterPrompt != "route carefully" {
		t.Fatalf("PassiveReplyRouterPrompt = %q", cfg.PassiveReplyRouterPrompt)
	}
	if cfg.PassiveReplyPrompt != "reply naturally" {
		t.Fatalf("PassiveReplyPrompt = %q", cfg.PassiveReplyPrompt)
	}
	if cfg.RecallReplyMode != assistant.RecallReplyModeOriginalForward {
		t.Fatalf("RecallReplyMode = %q", cfg.RecallReplyMode)
	}
	if cfg.LLMQQIDMaskingEnabled == nil || *cfg.LLMQQIDMaskingEnabled {
		t.Fatalf("LLMQQIDMaskingEnabled = %#v", cfg.LLMQQIDMaskingEnabled)
	}
	if cfg.RecentContextLimit != 11 || cfg.ContextSummaryThreshold != 37 {
		t.Fatalf("context limits = %d/%d", cfg.RecentContextLimit, cfg.ContextSummaryThreshold)
	}
	if cfg.PassiveReplyChance != 0.42 || cfg.PassiveReplyThreshold != 0.73 {
		t.Fatalf("passive settings = %v/%v", cfg.PassiveReplyChance, cfg.PassiveReplyThreshold)
	}
}

func TestLLMConfigFromEnvRestoresLegacyProviderSettings(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "openai_compatible")
	t.Setenv("LLM_API_KEY", "test-api-key")
	t.Setenv("LLM_BASE_URL", "https://api.example.test/v1")
	t.Setenv("LLM_API_FORMAT", "chat_completions")
	t.Setenv("LLM_MODEL", "gpt-test")
	t.Setenv("LLM_IMAGE_MODEL", "gpt-image-test")
	t.Setenv("LLM_IMAGE_BASE_URL", "https://image.example.test/v1")
	t.Setenv("LLM_IMAGE_ORIGIN", "203.0.113.10:443")
	t.Setenv("LLM_IMAGE_TIMEOUT_MS", "600000")
	t.Setenv("LLM_REASONING_EFFORT", "high")
	t.Setenv("LLM_CONTEXT_WINDOW_TOKENS", "200000")
	t.Setenv("LLM_MAX_CONTEXT_TOKENS", "12000")

	cfg := llmConfigFromEnv()
	if cfg.APIFormat != llm.APIFormatChatCompletions || cfg.ReasoningEffort != "high" {
		t.Fatalf("protocol settings = %q/%q", cfg.APIFormat, cfg.ReasoningEffort)
	}
	if cfg.ContextWindowTokens != 200000 || cfg.MaxContextTokens != 12000 {
		t.Fatalf("context budgets = %d/%d", cfg.ContextWindowTokens, cfg.MaxContextTokens)
	}
	if cfg.ImageBaseURL != "https://image.example.test/v1" || cfg.ImageOrigin != "203.0.113.10:443" || cfg.ImageTimeout != 10*time.Minute {
		t.Fatalf("image settings = %#v", cfg)
	}
}
