package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SuInk/diana/model/assistant"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedConnectionAPICreatesWithoutCredentialsAndRejectsSourceDeletion(t *testing.T) {
	source := assistant.DefaultBotConfig()
	source.OneBotAccessToken = "source-access-token"
	runtime := assistant.NewRuntime(source, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewBotHandlerWithFactory(ctx, runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	source = h.profiles.Profiles().Profiles[0]
	router := botTestRouter(h)
	post := func(path string, payload assistant.ConfigPayload) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		return response
	}
	draft := assistant.PayloadFromConfig(assistant.DefaultBotConfig())
	draft.ConnectionProfileID, draft.Name = source.ID, "复用机器人"
	response := post("/api/assistant/config/new", draft)
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	saved := h.profiles.Profiles().Profiles[len(h.profiles.Profiles().Profiles)-1]
	if saved.ConnectionProfileID != source.ID || saved.OneBotAccessToken != "" {
		t.Fatal("reference not stored independently of credentials")
	}
	response = post("/api/assistant/config/delete", assistant.ConfigPayload{ID: source.ID})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("delete referenced source: %d %s", response.Code, response.Body.String())
	}
	if len(h.profiles.Profiles().Profiles) != 2 {
		t.Fatal("failed deletion changed storage")
	}
	draft.ConnectionProfileID = "missing"
	response = post("/api/assistant/config/new", draft)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("accepted missing source: %d", response.Code)
	}
}

func TestWebSocketDuplicateSaveAndEnableAreRejectedWithoutMutation(t *testing.T) {
	source := assistant.DefaultBotConfig()
	source.Name, source.OneBotTransport, source.OneBotWSEndpoint = "已有机器人", assistant.OneBotTransportForwardWS, "ws://HOST:80"
	runtime := assistant.NewRuntime(source, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewBotHandlerWithFactory(ctx, runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	source = h.profiles.Profiles().Profiles[0]
	router := botTestRouter(h)
	post := func(path string, payload any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		return response
	}
	draft := assistant.PayloadFromConfig(source)
	draft.ID, draft.Name, draft.OneBotWSEndpoint = "", "另一台", "ws://host/"
	for _, enabled := range []bool{false, true} {
		draft.Enabled = enabled
		response := post("/api/assistant/config/new", draft)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("duplicate create: %d %s", response.Code, response.Body.String())
		}
		if len(h.profiles.Profiles().Profiles) != 1 {
			t.Fatal("rejected create changed storage")
		}
	}
	// Old clients saving their current bot without an ID must remain supported.
	draft.Enabled = false
	response := post("/api/assistant/config", draft)
	if response.Code != http.StatusOK || len(h.profiles.Profiles().Profiles) != 1 {
		t.Fatalf("legacy edit: %d %s", response.Code, response.Body.String())
	}
	// Seed a conflicting historical profile; enabling it must fail, but disabling
	// remains possible so users can repair old configurations.
	set := h.profiles.Profiles()
	duplicate := set.Profiles[0]
	duplicate.ID, duplicate.Name = "legacy", "旧重复配置"
	set.Profiles = append(set.Profiles, duplicate)
	if err := h.profiles.SaveProfiles(set); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/assistant/config/profile-enabled", "/api/assistant/config/profiles-enabled"} {
		response = post(path, map[string]any{"profile_id": duplicate.ID, "enabled": true})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("duplicate enable: %d %s", response.Code, response.Body.String())
		}
		current, _ := h.profiles.Profiles().ConfigForProfile(duplicate.ID)
		if current.Enabled {
			t.Fatal("rejected enable changed storage")
		}
	}
	response = post("/api/assistant/config/profile-enabled", map[string]any{"profile_id": duplicate.ID, "enabled": false})
	if response.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	// Explicitly selecting reuse fixes the historical duplicate.
	draft = assistant.PayloadFromConfig(duplicate)
	draft.ConnectionProfileID = source.ID
	response = post("/api/assistant/config", draft)
	if response.Code != http.StatusOK {
		t.Fatalf("reuse: %d %s", response.Code, response.Body.String())
	}
}
