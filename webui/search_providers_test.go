// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestSearchProviderAPIManagesKeysAndProtectsBindings(t *testing.T) {
	cfg := assistant.DefaultBotConfig()
	cfg.Enabled = false
	cfg.WebSearch = &assistant.WebSearchAssignment{ProviderIDs: []string{"browser"}, Disabled: true}
	runtime := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	router := botTestRouter(handler)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := request(http.MethodPost, "/api/assistant/search-providers", map[string]any{"name": "Private", "type": "search_mcp", "url": "https://private.example/mcp", "tool": "search", "api_key": "api-write-only-secret"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "api-write-only-secret") {
		t.Fatalf("save=%d %s", rec.Code, rec.Body.String())
	}
	var config assistant.SearchConfiguration
	config = assistant.SearchConfiguration{}
	json.Unmarshal(rec.Body.Bytes(), &config)
	saved := config.Providers[len(config.Providers)-1]
	if !saved.APIKeyConfigured {
		t.Fatal("key presence missing")
	}
	rec = request(http.MethodPost, "/api/assistant/search-providers", saved)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit=%s", rec.Body.String())
	}
	config = assistant.SearchConfiguration{}
	json.Unmarshal(rec.Body.Bytes(), &config)
	if !config.Providers[len(config.Providers)-1].APIKeyConfigured {
		t.Fatal("blank edit erased key")
	}
	saved.ClearAPIKey = true
	rec = request(http.MethodPost, "/api/assistant/search-providers", saved)
	config = assistant.SearchConfiguration{}
	json.Unmarshal(rec.Body.Bytes(), &config)
	if rec.Code != http.StatusOK || config.Providers[len(config.Providers)-1].APIKeyConfigured {
		t.Fatal("clear key failed")
	}
	if rec = request(http.MethodDelete, "/api/assistant/search-providers/tavily", nil); rec.Code != http.StatusOK {
		t.Fatalf("bound delete=%d", rec.Code)
	}
	if rec = request(http.MethodDelete, "/api/assistant/search-providers/"+saved.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("custom delete=%d %s", rec.Code, rec.Body.String())
	}
	if rec = request(http.MethodGet, "/api/assistant/search-providers?profile=unknown", nil); rec.Code != http.StatusOK {
		t.Fatalf("unknown profile=%d", rec.Code)
	}

}

func TestSearchProviderAPIDoesNotReportSuccessWhenPersistenceFails(t *testing.T) {
	cfg := assistant.DefaultBotConfig()
	cfg.Enabled = false
	runtime := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetSQLiteStore(db)
	db.Close()
	router := botTestRouter(handler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/search-providers", strings.NewReader(`{"name":"Custom","type":"search_mcp","url":"https://example.org/mcp","tool":"search","api_key":"test-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "test-secret") {
		t.Fatalf("save=%d %s", rec.Code, rec.Body.String())
	}
}

func TestSearchProviderDraftTestUsesStoredKeyWithoutSavingAndReturnsHTTPStatus(t *testing.T) {
	const secret = "stored-search-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Search-Token") != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"items":[{"href":"https://example.org/source","name":"Result","summary":"Found"}]}}`))
	}))
	defer server.Close()
	manager := assistant.NewDefaultPluginManager()
	id, err := manager.SaveSearchProvider(assistant.SearchProvider{Name: "Stored", Type: "http", URL: server.URL, APIKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(manager.Snapshot())
	cfg := assistant.DefaultBotConfig()
	cfg.Enabled = false
	runtime := assistant.NewRuntime(cfg, fakeChannel{}, manager, nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	router := botTestRouter(handler)
	draft := map[string]any{"id": id, "name": "Unsaved edit", "type": "http", "url": server.URL, "http_config": map[string]any{"auth_type": "header", "auth_header": "X-Search-Token", "results_path": "data.items", "url_path": "href", "title_path": "name", "snippet_path": "summary"}}
	for _, clear := range []bool{false, true} {
		draft["clear_api_key"] = clear
		raw, _ := json.Marshal(map[string]any{"provider": draft, "query": "Diana"})
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/search-providers/test", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var result struct {
			Error       string
			Content     string
			ResultCount int `json:"result_count"`
			HTTPStatus  int `json:"http_status"`
		}
		json.Unmarshal(rec.Body.Bytes(), &result)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("test=%d %s", rec.Code, rec.Body.String())
		}
		if !clear && (result.Error != "" || result.ResultCount != 1 || result.HTTPStatus != 200 || !strings.Contains(result.Content, "Found")) {
			t.Fatalf("result=%+v", result)
		}
		if clear && result.Error == "" {
			t.Fatal("clear-key draft used stored key")
		}
	}
	after, _ := json.Marshal(manager.Snapshot())
	if !bytes.Equal(before, after) {
		t.Fatal("draft test mutated persisted configuration")
	}
	// A caller may test without a provider name or saved ID.
	raw := `{"provider":{"type":"http","url":"` + server.URL + `","http_config":{"auth_type":"none"}},"query":"Diana"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/search-providers/test", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"http_status":401`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGlobalSearchAssignmentAPIAndPersistenceRollback(t *testing.T) {
	m := assistant.NewDefaultPluginManager()
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, m, nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	router := botTestRouter(h)
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/assistant/search-assignment?profile=unknown", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Fatalf("get=%d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, `{"provider_ids":["browser"],"disabled":true,"max_results":7}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"provider_ids":["browser"]`) {
		t.Fatalf("put=%d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, `{"provider_ids":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty=%d", rec.Code)
	}
	before := m.Snapshot()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assignment.db"))
	if err != nil {
		t.Fatal(err)
	}
	h.SetSQLiteStore(db)
	db.Close()
	if rec := request(http.MethodPut, `{"provider_ids":["exa"],"max_results":3}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed persist=%d %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("failed persistence changed global runtime")
	}
}

func TestRobotSearchOverrideAPIValidatesAndProtectsProviders(t *testing.T) {
	cfg := assistant.DefaultBotConfig()
	cfg.Enabled = false
	runtime := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	router := botTestRouter(handler)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := request(http.MethodPost, "/api/assistant/search-providers", map[string]any{"name": "Robot only", "type": "browser", "url": "https://www.bing.com/search", "query_param": "q"})
	var config assistant.SearchConfiguration
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &config) != nil {
		t.Fatalf("save provider=%d %s", rec.Code, rec.Body.String())
	}
	id := config.Providers[len(config.Providers)-1].ID
	current := handler.profiles.Profiles().Profiles[0]
	payload := assistant.PayloadFromConfig(current)
	payload.WebSearchOverride = &assistant.WebSearchAssignment{ProviderIDs: []string{"missing"}}
	if rec = request(http.MethodPost, "/api/assistant/config", payload); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing provider accepted: %d %s", rec.Code, rec.Body.String())
	}
	payload.WebSearchOverride = &assistant.WebSearchAssignment{ProviderIDs: []string{id}, MaxResults: 4}
	if rec = request(http.MethodPost, "/api/assistant/config", payload); rec.Code != http.StatusOK {
		t.Fatalf("save override=%d %s", rec.Code, rec.Body.String())
	}
	if saved, _ := handler.profiles.Profiles().ConfigForProfile(current.ID); saved.WebSearchOverride == nil || saved.WebSearchOverride.MaxResults != 4 {
		t.Fatalf("override not stored: %#v", saved.WebSearchOverride)
	}
	if rec = request(http.MethodDelete, "/api/assistant/search-providers/"+id, nil); rec.Code != http.StatusConflict {
		t.Fatalf("referenced provider deleted: %d %s", rec.Code, rec.Body.String())
	}
	payload.WebSearchOverride = nil
	if rec = request(http.MethodPost, "/api/assistant/config", payload); rec.Code != http.StatusOK {
		t.Fatalf("follow global=%d %s", rec.Code, rec.Body.String())
	}
	if rec = request(http.MethodDelete, "/api/assistant/search-providers/"+id, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete after follow global=%d %s", rec.Code, rec.Body.String())
	}
}
