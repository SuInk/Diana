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
	if rec = request(http.MethodDelete, "/api/assistant/search-providers/browser", nil); rec.Code != http.StatusConflict {
		t.Fatalf("bound delete=%d", rec.Code)
	}
	if rec = request(http.MethodDelete, "/api/assistant/search-providers/"+saved.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("custom delete=%d %s", rec.Code, rec.Body.String())
	}
	if rec = request(http.MethodGet, "/api/assistant/search-providers?profile=unknown", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile=%d", rec.Code)
	}
	if err := handler.validateSearchAssignment(&assistant.WebSearchAssignment{ProviderIDs: []string{saved.ID}}); err == nil {
		t.Fatal("deleted provider accepted")
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
