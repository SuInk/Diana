// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestCodingAgentProfilesHTTPPersistence(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := assistant.NewDefaultPluginManager()
	runtime := assistant.NewRuntime(assistant.BotConfig{ID: "a"}, fakeChannel{}, manager, nil, nil, nil, nil)
	handler := NewBotHandler(ctx, runtime)
	handler.SetSQLiteStore(db)
	router := botTestRouter(handler)
	post := func(body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/official.coding-agent/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret-test-value") {
			t.Fatal("legacy credential leaked")
		}
	}
	post(`{"settings":{"api_key":"secret-test-value","agents":[{"id":"code","backend":"codex","approval_mode":"off","default":true},{"id":"review","backend":"claude","approval_mode":"dangerous"}]}}`, 200)
	post(`{"settings":{"agents":[{"id":"bad","backend":"codex","approval_mode":"dangerous"}]}}`, 400)
	saved, ok, err := db.LoadPluginStates(ctx)
	if err != nil || !ok {
		t.Fatal("settings did not persist")
	}
	restored := assistant.NewDefaultPluginManager()
	restored.Restore(saved)
	state, ok := restored.Get("official.coding-agent")
	if !ok {
		t.Fatal("plugin not restored")
	}
	payload, err := json.Marshal(state.Settings["agents"])
	if err != nil {
		t.Fatal(err)
	}
	var agents []map[string]any
	if err := json.Unmarshal(payload, &agents); err != nil || len(agents) != 2 || agents[0]["id"] != "code" || agents[0]["default"] != true || agents[1]["id"] != "review" {
		t.Fatalf("restore lost configuration: %s", payload)
	}
	post(`{"settings":{"agents":[]}}`, 200)
	saved, ok, err = db.LoadPluginStates(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	restored.Restore(saved)
	state, _ = restored.Get("official.coding-agent")
	payload, _ = json.Marshal(state.Settings["agents"])
	if string(payload) != "[]" {
		t.Fatalf("clearing profiles not persisted: %s", payload)
	}
}

func TestCodingSetupHTTPUsesPersistedCredentialAndRedactsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dir, "app.db"))
	cli := filepath.Join(dir, "codex")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n[ \"$1\" = \"--version\" ] && exit 0\n[ \"$CODEX_API_KEY\" = \"isolated-secret-key\" ] || exit 2\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"DIANA_CONNECTION_OK\"}}' '{\"type\":\"turn.completed\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	manager := assistant.NewDefaultPluginManager()
	rt := assistant.NewRuntime(assistant.BotConfig{ID: "a"}, fakeChannel{}, manager, nil, nil, nil, nil)
	handler := NewBotHandler(context.Background(), rt)
	router := botTestRouter(handler)
	body, _ := json.Marshal(map[string]any{"settings": map[string]any{"agents": []map[string]any{{"id": "code", "backend": "codex", "command": cli, "approval_mode": "off"}}, "agent_api_keys": `{"code":"isolated-secret-key"}`}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/official.coding-agent/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "isolated-secret-key") {
		t.Fatalf("unsafe save: %d", rec.Code)
	}
	for _, op := range []string{"status", "test"} {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/coding-agent/setup", strings.NewReader(`{"agent":"code","operation":"`+op+`"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "isolated-secret-key") {
			t.Fatalf("setup %s: %d %s", op, rec.Code, rec.Body.String())
		}
		if op == "test" && !strings.Contains(rec.Body.String(), "连接成功") {
			t.Fatal("saved credential was not used")
		}
	}
}
