// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

type resolverCredentialTestPlugin struct {
	*assistant.ResolverPlugin
	keys     []string
	settings []assistant.SettingValues
}

func (p *resolverCredentialTestPlugin) TestCredentialsForKey(_ context.Context, settings assistant.SettingValues, key string) []assistant.CredentialCheck {
	p.keys = append(p.keys, key)
	p.settings = append(p.settings, settings)
	keys := []string{key}
	if key == "" {
		keys = []string{"bili_sessdata", "douyin_cookie", "xhs_cookie", "ytdlp_cookies_path"}
	}
	checks := make([]assistant.CredentialCheck, 0, len(keys))
	for _, k := range keys {
		configured := settings.String(k, "") != ""
		state := assistant.CredentialUnconfigured
		if configured {
			state = assistant.CredentialUnverified
		}
		checks = append(checks, assistant.CredentialCheck{Key: k, Configured: configured, State: state, Message: "mock"})
	}
	return checks
}

func TestResolverCredentialsHTTPFilteringAndDrafts(t *testing.T) {
	ctx := context.Background()
	plugin := &resolverCredentialTestPlugin{ResolverPlugin: assistant.NewResolverPlugin(nil)}
	manager := assistant.NewPluginManager(plugin)
	if _, err := manager.UpdateSettings(assistant.ResolverPluginID, map[string]any{
		"bili_sessdata": "saved-bili-secret", "douyin_cookie": "saved-douyin-secret", "xhs_cookie": "saved-xhs-secret",
		"ytdlp_cookies_path": "saved-cookie-file", "enabled_platforms": []string{},
	}); err != nil {
		t.Fatal(err)
	}
	runtime := assistant.NewRuntime(assistant.BotConfig{ID: "resolver-test"}, fakeChannel{}, manager, nil, nil, nil, nil)
	handler := NewBotHandler(ctx, runtime)
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler.SetSQLiteStore(store)
	if err := handler.persistState(); err != nil {
		t.Fatal(err)
	}
	before := manager.Snapshot()
	savedBefore, _, err := store.LoadPluginStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	router := botTestRouter(handler)
	cases := []struct {
		name, query, body, key, value string
		status, count                 int
	}{
		{"legacy all", "", `{}`, "", "", 200, 4},
		{"empty secret reuses saved", "credential_key=bili_sessdata", `{"settings":{"bili_sessdata":""}}`, "bili_sessdata", "saved-bili-secret", 200, 1},
		{"whitespace secret reuses saved", "credential_key=douyin_cookie", `{"settings":{"douyin_cookie":"  "}}`, "douyin_cookie", "saved-douyin-secret", 200, 1},
		{"draft overrides saved", "credential_key=xhs_cookie", `{"settings":{"xhs_cookie":"draft-xhs-secret"}}`, "xhs_cookie", "draft-xhs-secret", 200, 1},
		{"clear wins over draft", "credential_key=bili_sessdata", `{"settings":{"bili_sessdata":"draft-bili-secret"},"clear_secrets":["bili_sessdata"]}`, "bili_sessdata", "", 200, 1},
		{"file draft", "credential_key=ytdlp_cookies_path", `{"settings":{"ytdlp_cookies_path":"draft-cookie-file"}}`, "ytdlp_cookies_path", "draft-cookie-file", 200, 1},
		{"empty nonsecret clears", "credential_key=ytdlp_cookies_path", `{"settings":{"ytdlp_cookies_path":""}}`, "ytdlp_cookies_path", "", 200, 1},
		{"invalid key", "credential_key=proxy_url", `{}`, "", "", 400, 0},
		{"explicit empty key", "credential_key=", `{}`, "", "", 400, 0},
		{"padded key", "credential_key=" + url.QueryEscape(" bili_sessdata "), `{}`, "", "", 400, 0},
		{"duplicate keys", "credential_key=bili_sessdata&credential_key=xhs_cookie", `{}`, "", "", 400, 0},
		{"invalid body", "credential_key=bili_sessdata", `{`, "", "", 400, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callsBefore := len(plugin.keys)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/resolver/test?"+tc.query, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			if recorder.Code != tc.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if tc.status != http.StatusOK {
				if len(plugin.keys) != callsBefore {
					t.Fatal("invalid request executed credential checks")
				}
				return
			}
			if len(plugin.keys) != callsBefore+1 || plugin.keys[callsBefore] != tc.key {
				t.Fatalf("keys=%v", plugin.keys)
			}
			settings := plugin.settings[callsBefore]
			if tc.key != "" && settings.String(tc.key, "") != tc.value {
				t.Fatalf("effective %s=%q, want %q", tc.key, settings.String(tc.key, ""), tc.value)
			}
			if len(settings.StringSlice("enabled_platforms")) != 0 {
				t.Fatal("test should run even with every platform disabled")
			}
			var body struct {
				Credentials []assistant.CredentialCheck `json:"credentials"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || len(body.Credentials) != tc.count {
				t.Fatalf("response=%s err=%v", recorder.Body.String(), err)
			}
			if tc.key != "" && (body.Credentials[0].Key != tc.key || body.Credentials[0].Configured != (tc.value != "")) {
				t.Fatalf("credentials=%v", body.Credentials)
			}
			if strings.Contains(recorder.Body.String(), "-secret") {
				t.Fatal("response leaked a credential")
			}
		})
	}
	if !reflect.DeepEqual(before, manager.Snapshot()) {
		t.Fatal("credential tests changed in-memory settings")
	}
	savedAfter, _, err := store.LoadPluginStates(ctx)
	if err != nil || !reflect.DeepEqual(savedBefore, savedAfter) {
		t.Fatalf("credential tests persisted settings: err=%v", err)
	}
}
