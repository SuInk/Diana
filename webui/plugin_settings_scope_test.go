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

func TestPluginProfileSettingsHTTPAndPersistence(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := assistant.NewDefaultPluginManager()
	r := assistant.NewRuntime(assistant.BotConfig{ID: "a"}, fakeChannel{}, m, nil, nil, nil, nil)
	h := NewBotHandler(ctx, r)
	profiles := NewMemoryBotProfileStore(r.Config())
	if err := profiles.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{{ID: "a", Platform: assistant.PlatformOneBotV11}, {ID: "b", Platform: assistant.PlatformTelegram}}}); err != nil {
		t.Fatal(err)
	}
	h.SetProfileStore(profiles)
	h.SetSQLiteStore(db)
	router := botTestRouter(h)
	id := assistant.ResolverPluginID
	post := func(profile, body string, status int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/"+id+"/settings?profile="+profile, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "test-secret") {
			t.Fatal("secret leaked")
		}
	}
	post("", `{"settings":{"max_images":4,"douyin_cookie":"test-secret-default"}}`, 200)
	post("a", `{"settings":{"max_images":6,"douyin_cookie":"test-secret-a"}}`, 200)
	post("b", `{"settings":{"max_images":8}}`, 200)
	post("missing", `{"settings":{"max_images":10}}`, 404)
	for _, tc := range []struct {
		profile string
		images  float64
	}{{"a", 8}, {"b", 8}} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins?profile="+tc.profile, nil))
		if strings.Contains(rec.Body.String(), "test-secret") || strings.Contains(rec.Body.String(), "profile_settings") {
			t.Fatal("scope credentials leaked")
		}
		var states []assistant.PluginState
		if err := json.Unmarshal(rec.Body.Bytes(), &states); err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if state.Manifest.ID == id && state.Settings["max_images"] != tc.images {
				t.Fatalf("wrong profile settings: %s", tc.profile)
			}
		}
	}
	saved, ok, err := db.LoadPluginStates(ctx)
	if err != nil || !ok {
		t.Fatal("settings not persisted")
	}
	restored := assistant.NewDefaultPluginManager()
	restored.Restore(saved)
	if restored.MigrateProfileConfigurations([]assistant.BotConfig{{ID: "a"}, {ID: "b"}, {ID: "new"}}) {
		t.Fatal("SQLite migration marker lost")
	}
	_, newSettings, _ := restored.PluginWithSettingsForProfile(id, "new")
	if newSettings.String("douyin_cookie", "") != "test-secret-a" || newSettings.Int("max_images", 0) != 8 {
		t.Fatal("new profile inherited settings")
	}
	_, settings, _ := restored.PluginWithSettingsForProfile(id, "a")
	if settings.String("douyin_cookie", "") != "test-secret-a" {
		t.Fatal("restore lost robot credential")
	}
	post("a", `{"settings":{},"clear_secrets":["douyin_cookie"]}`, 200)
	post("a", `{"inherit":true}`, 400)
	state, _ := m.Get(id)
	if state.ForProfile("a").Settings["douyin_cookie"] != nil || len(state.Settings) != 0 {
		t.Fatal("global credential survived")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins", nil))
	var systemPlugins []assistant.PluginState
	if err := json.Unmarshal(rec.Body.Bytes(), &systemPlugins); err != nil || len(systemPlugins) < 2 {
		t.Fatal("unscoped list did not expose shared configuration")
	}
	foundOpenAPI := false
	for _, plugin := range systemPlugins {
		foundOpenAPI = foundOpenAPI || plugin.Manifest.ID == assistant.OpenAPIPluginID
	}
	if !foundOpenAPI {
		t.Fatal("system settings lost the OpenAPI configuration")
	}
}

func TestGroupAdminSessionBindsRobotProfile(t *testing.T) {
	ctx := context.Background()
	r := assistant.NewRuntime(assistant.BotConfig{ID: "a"}, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(ctx, r)
	profiles := NewMemoryBotProfileStore(r.Config())
	profiles.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{{ID: "a"}, {ID: "b"}}})
	h.SetProfileStore(profiles)
	store := NewMemoryBotGroupConfigStore()
	h.SetGroupConfigStore(store)
	store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "a", GroupID: "100", SystemPrompt: "a only"}, r.Config())
	store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "b", GroupID: "100", SystemPrompt: "b only"}, r.Config())
	code, _, err := h.groupAdmin.CreateChallenge("100", "200", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.groupAdmin.Verify("100", "200", code, "a"); err == nil {
		t.Fatal("cross-profile challenge accepted")
	}
	token, _, err := h.groupAdmin.Verify("100", "200", code, "b")
	if err != nil {
		t.Fatal(err)
	}
	router := botTestRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/assistant/group-admin/config", nil)
	req.Header.Set("X-Diana-Group-Token", token)
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "b only") || strings.Contains(rec.Body.String(), "a only") {
		t.Fatalf("wrong group config: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/assistant/group-admin/config", strings.NewReader(`{"config":{"bot_profile_id":"a","system_prompt":"updated b"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Diana-Group-Token", token)
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save failed: %s", rec.Body.String())
	}
	a, _ := store.ConfigForGroup("a", "100")
	b, _ := store.ConfigForGroup("b", "100")
	if a.SystemPrompt != "a only" || b.SystemPrompt != "updated b" {
		t.Fatal("session wrote another profile")
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/group-admin/challenge", strings.NewReader(`{"group_id":"100","user_id":"200"}`)))
	if rec.Code != 400 {
		t.Fatalf("ambiguous profile accepted: %d", rec.Code)
	}
}

type groupAdminScopeChannel struct {
	fakeChannel
	actions []string
}

func (c *groupAdminScopeChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.actions = append(c.actions, action)
	return map[string]any{"role": "admin", "message_id": 42}, nil
}

func TestGroupAdminVerificationAndCodeUseSelectedBot(t *testing.T) {
	a, b := &groupAdminScopeChannel{}, &groupAdminScopeChannel{}
	channel := assistant.NewMultiChannel([]assistant.ChannelBinding{{ProfileID: "a", Platform: assistant.PlatformOneBotV11, Channel: a}, {ProfileID: "b", Platform: assistant.PlatformOneBotV11, Channel: b}})
	r := assistant.NewRuntime(assistant.BotConfig{ID: "a", Platform: assistant.PlatformOneBotV11}, channel, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), r)
	profiles := NewMemoryBotProfileStore(r.Config())
	if err := profiles.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{{ID: "a", Platform: assistant.PlatformOneBotV11}, {ID: "b", Platform: assistant.PlatformOneBotV11}}}); err != nil {
		t.Fatal(err)
	}
	h.SetProfileStore(profiles)
	router := botTestRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/group-admin/challenge", strings.NewReader(`{"profile_id":"b","group_id":"100","user_id":"200"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || len(a.actions) != 0 || len(b.actions) != 2 || b.actions[0] != "get_group_member_info" || b.actions[1] != "send_private_msg" {
		t.Fatalf("wrong scope: status=%d a=%v b=%v body=%s", rec.Code, a.actions, b.actions, rec.Body.String())
	}
}
