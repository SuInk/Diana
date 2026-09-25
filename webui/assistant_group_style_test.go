// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type groupStyleStubRuntime struct {
	BotRuntime
	style        assistant.GroupStyle
	found        bool
	relearnErr   error
	savedText    string
	gotProfileID string
}

func (r *groupStyleStubRuntime) ProfileConfig(id string) assistant.BotConfig {
	enabled := true
	return assistant.BotConfig{ID: "bot", ExpressionLearningEnabled: &enabled}
}

func (r *groupStyleStubRuntime) GroupStyleForProfile(_ context.Context, profileID, groupID string) (assistant.GroupStyle, bool, error) {
	r.gotProfileID = profileID
	return r.style, r.found, nil
}

func (r *groupStyleStubRuntime) SaveGroupStyleForProfile(_ context.Context, profileID, groupID, text string) (assistant.GroupStyle, bool, error) {
	r.savedText = text
	if strings.TrimSpace(text) == "" {
		return assistant.GroupStyle{}, false, nil
	}
	return assistant.GroupStyle{ProfileID: profileID, GroupID: groupID, Text: text, Manual: true}, true, nil
}

func (r *groupStyleStubRuntime) RelearnGroupStyle(_ context.Context, profileID, groupID string) (assistant.GroupStyle, error) {
	if r.relearnErr != nil {
		return assistant.GroupStyle{}, r.relearnErr
	}
	return assistant.GroupStyle{ProfileID: profileID, GroupID: groupID, Text: "重新学到的"}, nil
}

func newGroupStyleRouter(runtime BotRuntime) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := &BotHandler{runtime: runtime}
	router := gin.New()
	handler.registerGroupStyleRoutes(router)
	return router
}

func decodeGroupStyle(t *testing.T, rec *httptest.ResponseRecorder) groupStyleResponse {
	t.Helper()
	var response groupStyleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("body = %s", rec.Body.String())
	}
	return response
}

func TestGroupStyleRoutes(t *testing.T) {
	runtime := &groupStyleStubRuntime{style: assistant.GroupStyle{Text: "爱说绷不住了"}, found: true}
	router := newGroupStyleRouter(runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups/g1/style", nil))
	got := decodeGroupStyle(t, rec)
	if rec.Code != http.StatusOK || got.Style == nil || got.Style.Text != "爱说绷不住了" || !got.LearningEnabled || runtime.gotProfileID != "bot" {
		t.Fatalf("get: %d %#v profile=%q", rec.Code, got, runtime.gotProfileID)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/assistant/groups/g1/style?bot_profile_id=bot", strings.NewReader(`{"text":"主人写的"}`)))
	if got := decodeGroupStyle(t, rec); rec.Code != http.StatusOK || got.Style == nil || !got.Style.Manual || runtime.savedText != "主人写的" {
		t.Fatalf("put: %d %#v", rec.Code, got)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/assistant/groups/g1/style", strings.NewReader(`{"text":"`+strings.Repeat("长", assistant.GroupStyleMaxRunes+1)+`"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long note status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/groups/g1/style/relearn", nil))
	if got := decodeGroupStyle(t, rec); rec.Code != http.StatusOK || got.Style == nil || got.Style.Text != "重新学到的" {
		t.Fatalf("relearn: %d %#v", rec.Code, got)
	}

	runtime.relearnErr = assistant.ErrGroupStyleNotEnoughMessages
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/groups/g1/style/relearn", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "不够") {
		t.Fatalf("not enough messages: %d %s", rec.Code, rec.Body.String())
	}
}
