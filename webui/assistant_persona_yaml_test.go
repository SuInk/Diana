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
)

func personaYAMLRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BotHandler{}
	router.POST("/api/assistant/personas/yaml", handler.renderPersonaYAML)
	router.POST("/api/assistant/personas/parse", handler.parsePersonaSource)
	router.POST("/api/assistant/personas/import", handler.importPersonas)
	return router
}

func postJSON(t *testing.T, router *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
	return recorder
}

// 编辑器的往返：渲染出来的 YAML 原样送回解析，改过的提示词还在，没改过的不变成覆盖。
func TestPersonaYAMLRenderAndParse(t *testing.T) {
	router := personaYAMLRouter()
	rendered := postJSON(t, router, "/api/assistant/personas/yaml", map[string]any{"personas": []map[string]any{{
		"name": "猫娘", "system_prompt": "喵", "prompts": map[string]string{"reply.wake_only": "叫我就接着说"},
	}}})
	if rendered.Code != http.StatusOK {
		t.Fatalf("render status = %d body = %s", rendered.Code, rendered.Body.String())
	}
	var yamlResponse struct {
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(rendered.Body.Bytes(), &yamlResponse); err != nil {
		t.Fatal(err)
	}
	parsed := postJSON(t, router, "/api/assistant/personas/parse", map[string]string{"source": yamlResponse.YAML})
	if parsed.Code != http.StatusOK {
		t.Fatalf("parse status = %d body = %s", parsed.Code, parsed.Body.String())
	}
	var parseResponse struct {
		Personas []struct {
			Name    string            `json:"name"`
			Prompts map[string]string `json:"prompts"`
		} `json:"personas"`
	}
	if err := json.Unmarshal(parsed.Body.Bytes(), &parseResponse); err != nil {
		t.Fatal(err)
	}
	if len(parseResponse.Personas) != 1 || len(parseResponse.Personas[0].Prompts) != 1 || parseResponse.Personas[0].Prompts["reply.wake_only"] != "叫我就接着说" {
		t.Fatalf("parsed = %s", parsed.Body.String())
	}

	broken := strings.Replace(yamlResponse.YAML, "\n  reply.image_only: |-", "\n  reply.imag_only: |-", 1)
	rejected := postJSON(t, router, "/api/assistant/personas/parse", map[string]string{"source": broken})
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "reply.imag_only") {
		t.Fatalf("misspelled key accepted: %d %s", rejected.Code, rejected.Body.String())
	}
}

// 前端把 JSON 文件解析好再发 personas 时，也不能绕过完整性检查。
func TestPersonaImportRejectsPartialPromptsFromJSON(t *testing.T) {
	recorder := postJSON(t, personaYAMLRouter(), "/api/assistant/personas/import", map[string]any{"personas": []map[string]any{{
		"name": "半套", "system_prompt": "喵", "prompts": map[string]string{"reply.wake_only": "叫我就接着说"},
	}}})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "缺少") {
		t.Fatalf("partial prompts imported: %d %s", recorder.Code, recorder.Body.String())
	}
}
