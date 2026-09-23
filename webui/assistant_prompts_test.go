// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/SuInk/diana/model/assistant"
)

// 界面上的原文、分组和上限都从这个接口来，必须和登记表是同一份。
func TestPromptCatalogServesRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/assistant/prompts", (&BotHandler{}).promptCatalog)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/prompts", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Groups   []assistant.PromptGroupInfo `json:"groups"`
		Prompts  []assistant.PromptSpec      `json:"prompts"`
		MaxRunes int                         `json:"max_runes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Prompts) != len(assistant.PromptSpecs()) || len(payload.Groups) != len(assistant.PromptGroups()) {
		t.Fatalf("catalog has %d prompts / %d groups, registry has %d / %d", len(payload.Prompts), len(payload.Groups), len(assistant.PromptSpecs()), len(assistant.PromptGroups()))
	}
	if payload.MaxRunes != assistant.PromptOverrideMaxRunes {
		t.Fatalf("max_runes = %d", payload.MaxRunes)
	}
	for _, spec := range payload.Prompts {
		if spec.Key == "reply.wake_only" && spec.Default != "" {
			return
		}
	}
	t.Fatal("wake-only prompt missing from catalog")
}
