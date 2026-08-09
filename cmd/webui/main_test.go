package main

import (
	"net/http"
	"net/http/httptest"
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
