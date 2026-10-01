// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// TestConsoleGroupsKeepDisabledMode 停用档位要能存、能读回，不认识这个字段的旧页面
// 保存时不冲掉它，列表那排开关来回拨也不丢。
func TestConsoleGroupsKeepDisabledMode(t *testing.T) {
	base := assistant.DefaultBotConfig()
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	store := NewMemoryBotGroupConfigStore()
	handler := NewBotHandler(context.Background(), runtime)
	handler.SetGroupConfigStore(store)
	router := botTestRouter(handler)

	post := func(path, body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	mode := func() assistant.GroupDisabledMode {
		t.Helper()
		for _, saved := range store.Groups().Groups {
			if saved.GroupID == "50006" {
				return saved.DisabledMode
			}
		}
		t.Fatal("group config not saved")
		return ""
	}

	post("/api/assistant/groups", `{"config":{"group_id":"50006","enabled":false,"enabled_set":true,"disabled_mode":"observe"}}`)
	if got := mode(); got != assistant.GroupDisabledObserve {
		t.Fatalf("disabled_mode = %q, want observe", got)
	}
	// 旧页面不带这个字段：沿用已存的档位。
	post("/api/assistant/groups", `{"config":{"group_id":"50006","enabled":false,"enabled_set":true}}`)
	if got := mode(); got != assistant.GroupDisabledObserve {
		t.Fatalf("legacy save reset disabled_mode to %q", got)
	}
	// 列表开关打开再关上，档位留着。
	post("/api/assistant/groups/switches", `{"group_ids":["50006"],"enabled":true}`)
	post("/api/assistant/groups/switches", `{"group_ids":["50006"],"enabled":false}`)
	if got := mode(); got != assistant.GroupDisabledObserve {
		t.Fatalf("switch toggle changed disabled_mode to %q", got)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
	if !strings.Contains(rec.Body.String(), `"disabled_mode":"observe"`) {
		t.Fatalf("group list does not carry disabled_mode: %s", rec.Body.String())
	}
	// 显式选回休眠；认不出的值按休眠处理。
	post("/api/assistant/groups", `{"config":{"group_id":"50006","enabled":false,"enabled_set":true,"disabled_mode":"dormant"}}`)
	if got := mode(); got != assistant.GroupDisabledDormant {
		t.Fatalf("disabled_mode = %q, want dormant", got)
	}
	if got := assistant.GroupDisabledMode("whatever").Normalized(); got != "" {
		t.Fatalf("unknown mode normalized to %q, want empty", got)
	}
}
