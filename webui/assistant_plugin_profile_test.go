package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestBotHandlerPluginProfileIsolationPersists(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := assistant.NewDefaultPluginManager()
	r := assistant.NewRuntime(assistant.BotConfig{ID: "qq-a"}, fakeChannel{}, m, nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(ctx, r, nil)
	profiles := NewMemoryBotProfileStore(r.Config())
	if err := profiles.SaveProfiles(assistant.ProfileSet{ActiveID: "qq-a", Profiles: []assistant.BotConfig{
		{ID: "qq-a", Platform: assistant.PlatformOneBotV11},
		{ID: "qq-b", Platform: assistant.PlatformOneBotV11},
		{ID: "tg", Platform: assistant.PlatformTelegram},
	}}); err != nil {
		t.Fatal(err)
	}
	h.SetProfileStore(profiles)
	h.SetSQLiteStore(store)
	router := botTestRouter(h)
	id := assistant.ResolverPluginID
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/"+id+"/enabled?profile=qq-a", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
	}
	for _, profile := range []string{"qq-a", "qq-b", "tg", ""} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins?profile="+profile, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var states []assistant.PluginState
		if err := json.Unmarshal(rec.Body.Bytes(), &states); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, state := range states {
			if state.Manifest.ID == id {
				found = true
				if state.Enabled != (profile != "qq-a") {
					t.Fatalf("profile %q enabled = %v", profile, state.Enabled)
				}
			}
		}
		if !found {
			t.Fatal("plugin missing from list")
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/assistant/plugins?profile=missing"
		if method == http.MethodPost {
			path = "/api/assistant/plugins/" + id + "/enabled?profile=missing"
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{"enabled":false}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown profile: %d %s", rec.Code, rec.Body.String())
		}
	}
	saved, ok, err := store.LoadPluginStates(ctx)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	restored := assistant.NewDefaultPluginManager()
	restored.Restore(saved)
	if restored.EnabledWithOverrides(id, restored.ProfileOverrides("qq-a")) || !restored.EnabledWithOverrides(id, restored.ProfileOverrides("tg")) {
		t.Fatal("persisted switches lost profile isolation")
	}
}
