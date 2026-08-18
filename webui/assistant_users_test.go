// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func newAssistantUsersTestRouter(t *testing.T) (*storage.SQLiteStore, http.Handler) {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewQQBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	handler.SetSQLiteStore(store)
	return store, qqBotTestRouter(handler)
}

func TestAssistantUsersListAndDetail(t *testing.T) {
	ctx := context.Background()
	store, router := newAssistantUsersTestRouter(t)

	events := []assistant.MessageEvent{
		{Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10001", SenderName: "小明", MessageID: "m1", RawMessage: "我最喜欢打羽毛球了", Time: 1_700_000_000},
		{Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10002", SenderName: "阿花", MessageID: "m2", RawMessage: "下周要去上海出差", Time: 1_700_000_100},
	}
	for _, event := range events {
		if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateUserMemory(ctx, events[1], assistant.UserMemoryUpdate{
		FavorabilityDelta:        2,
		FavorabilityChangeSource: "interaction",
		FavorabilityChangeReason: "帮忙解答了问题",
		Administrative:           true,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/assistant/users", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listResp assistantUsersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Total != 2 || len(listResp.Users) != 2 {
		t.Fatalf("total=%d users=%d, want 2/2", listResp.Total, len(listResp.Users))
	}
	// 最近更新的在最前，且列表不携带记忆正文，只带条数。
	if listResp.Users[0].UserID != "10002" {
		t.Fatalf("first user=%s, want 10002", listResp.Users[0].UserID)
	}
	if listResp.Users[0].Memories != nil {
		t.Fatalf("list should omit memory bodies, got %#v", listResp.Users[0].Memories)
	}
	if listResp.Users[0].MemoryCount != 1 {
		t.Fatalf("memory_count=%d, want 1", listResp.Users[0].MemoryCount)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/assistant/users?q=小明", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Total != 1 || len(listResp.Users) != 1 || listResp.Users[0].UserID != "10001" {
		t.Fatalf("filtered result=%#v, want only 10001", listResp.Users)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/assistant/users/10002", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail assistantUserDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Profile.UserID != "10002" || detail.Profile.DisplayName != "阿花" {
		t.Fatalf("profile=%#v", detail.Profile)
	}
	if len(detail.Profile.Memories) != 1 || detail.Profile.Memories[0].Text != "下周要去上海出差" {
		t.Fatalf("memories=%#v", detail.Profile.Memories)
	}
	if len(detail.FavorabilityChanges) != 1 || detail.FavorabilityChanges[0].Delta != 2 {
		t.Fatalf("changes=%#v", detail.FavorabilityChanges)
	}
}

func TestAssistantUsersNotFoundAndNoStore(t *testing.T) {
	_, router := newAssistantUsersTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/assistant/users/99999", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}

	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewQQBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	bare := qqBotTestRouter(handler)
	req = httptest.NewRequest(http.MethodGet, "/api/assistant/users", nil)
	rec = httptest.NewRecorder()
	bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503 when sqlite is missing", rec.Code)
	}
}
