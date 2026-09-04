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

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// TestAppLogHandlerListsLogs 验证对应功能场景。
func TestAppLogHandlerListsLogs(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if err := store.AppendLog(ctx, storage.AppLogEntry{
		Kind:    storage.LogKindOperation,
		Action:  "assistant.start",
		Message: "started",
		Target:  "bot",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendLog(ctx, storage.AppLogEntry{
		Kind:    storage.LogKindError,
		Action:  "llm.test",
		Message: "failed",
		Detail:  "bad gateway",
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewAppLogHandler(store)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/api/logs?kind=error&limit=10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload appLogsResponse
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(payload.Logs) != 1 || payload.Logs[0].Kind != storage.LogKindError || payload.Logs[0].Action != "llm.test" {
		t.Fatalf("logs = %#v", payload.Logs)
	}
}

// TestAppLogHandlerRejectsInvalidKind 验证对应功能场景。
func TestAppLogHandlerRejectsInvalidKind(t *testing.T) {
	handler := NewAppLogHandler(nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/api/logs?kind=debug", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestLLMConfigHandlerWritesAppLogs 验证对应功能场景。
func TestLLMConfigHandlerWritesAppLogs(t *testing.T) {
	ctx := context.Background()
	logStore, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logStore.Close() }()

	profileStore := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key-123",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(profileStore)
	handler.SetLogStore(logStore)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	okBody := []byte(`{"id":"` + profileStore.Profiles().Profiles[0].ID + `","provider":"openai_compatible","api_key":"new-key-123","model":"gp5.5"}`)
	okReq := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(okBody))
	okReq.Header.Set("X-Diana-Actor", "admin")
	okRec := httptest.NewRecorder()
	router.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("ok status = %d, body = %s", okRec.Code, okRec.Body.String())
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader([]byte(`{"provider":"openai_compatible","api_key":"short","model":"gp5.5"}`)))
	badReq.RemoteAddr = "203.0.113.9:1234"
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d, body = %s", badRec.Code, badRec.Body.String())
	}

	operations, err := logStore.ListLogs(ctx, storage.AppLogFilter{Kind: storage.LogKindOperation, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	errors, err := logStore.ListLogs(ctx, storage.AppLogFilter{Kind: storage.LogKindError, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Action != "llm.config.save" {
		t.Fatalf("operation logs = %#v", operations)
	}
	if operations[0].Actor != "admin" {
		t.Fatalf("operation actor = %q", operations[0].Actor)
	}
	if len(errors) != 1 || errors[0].Action != "llm.config.save" {
		t.Fatalf("error logs = %#v", errors)
	}
	if errors[0].Actor != "web:203.0.113.9" {
		t.Fatalf("error actor = %q", errors[0].Actor)
	}
}

func TestProviderTestReturnsAndLogsRedactedUpstreamError(t *testing.T) {
	const apiKey = "super-secret-api-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("temporary upstream failure for " + apiKey))
	}))
	defer upstream.Close()

	ctx := context.Background()
	logStore, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logStore.Close() }()

	profileStore := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider:  llm.ProviderOpenAICompatible,
		APIFormat: llm.APIFormatChatCompletions,
		APIKey:    apiKey,
		BaseURL:   upstream.URL,
		Model:     "chat-model",
	})
	profile := profileStore.Profiles().Profiles[0]
	handler := NewLLMConfigHandler(profileStore)
	handler.SetLogStore(logStore)
	router := testRouter(handler)

	body := `{"providerId":"` + profile.ID + `","modelId":"` + profile.ID + `:chat-model","message":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/llm/providers/test", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "提供商测试失败") || !strings.Contains(rec.Body.String(), "temporary upstream failure") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), apiKey) {
		t.Fatalf("response leaked API key: %s", rec.Body.String())
	}

	entries, err := logStore.ListLogs(ctx, storage.AppLogFilter{Kind: storage.LogKindError, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("error logs = %#v", entries)
	}
	entry := entries[0]
	if entry.Action != "llm.providers.test" || entry.Target != profile.ID {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Metadata["provider_id"] != profile.ID || entry.Metadata["model_id"] != profile.ID+":chat-model" {
		t.Fatalf("metadata = %#v", entry.Metadata)
	}
	if strings.Contains(entry.Detail, apiKey) {
		t.Fatalf("log leaked API key: %s", entry.Detail)
	}
}
