// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// 导出再导入：界面上的覆盖表原样回来，登记表里没有的键报出来而不是整份拒收。
func TestPromptFileExportImport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BotHandler{}
	router.POST("/export", handler.exportPromptFile)
	router.POST("/import", handler.importPromptFile)
	key := assistant.PromptSpecs()[0].Key
	body, _ := json.Marshal(map[string]any{"overrides": map[string]string{key: "改过的正文"}})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/export", bytes.NewReader(body)))
	var exported struct {
		YAML string `json:"yaml"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &exported) != nil || !strings.Contains(exported.YAML, "改过的正文") {
		t.Fatalf("export status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	source := exported.YAML + "  not.a.real.key: 多出来的\n"
	body, _ = json.Marshal(map[string]string{"source": source})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(body)))
	var imported assistant.PromptFileImport
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &imported) != nil {
		t.Fatalf("import status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if imported.Changed != 1 || imported.Overrides[key] != "改过的正文" || len(imported.Unknown) != 1 {
		t.Fatalf("imported = %#v", imported)
	}
}

// 预览拿的是请求里那份还没保存的配置：覆盖、补充判据和闲聊档位都要反映在发出去的内容里。
func TestParticipationPromptPreviewUsesUnsavedConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/preview", (&BotHandler{}).previewParticipationPrompt)
	body, _ := json.Marshal(map[string]any{
		"name":                           "Diana",
		"proactive_reply_extra_criteria": "群里叫「鸽子」是催更",
		"prompt_overrides":               map[string]string{"routing.participation.relevance_true": "有人叫它小D"},
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview", bytes.NewReader(body)))
	var preview assistant.ParticipationPromptPreview
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &preview) != nil {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, want := range []string{"有人叫它小D", "群里叫「鸽子」是催更"} {
		if !strings.Contains(preview.System, want) {
			t.Fatalf("system prompt is missing %q:\n%s", want, preview.System)
		}
	}
	if !strings.Contains(preview.User, "【当前消息】") || len(preview.Decision) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
}
