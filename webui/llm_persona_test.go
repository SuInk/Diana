// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// echoPersonaClient 把收到的请求原样交还，便于断言提示词内容。
type echoPersonaClient struct {
	reply    string
	captured llm.GenerateRequest
}

func (client *echoPersonaClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	client.captured = req
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "gpt-test", Text: client.reply}, nil
}

func personaRouterWithClient(client llm.LLMClient) *gin.Engine {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "test-key",
		Model:    "gpt-test",
	})
	return testRouter(NewLLMConfigHandlerWithFactory(store, func(llm.ProviderConfig) (llm.LLMClient, error) {
		return client, nil
	}))
}

func postPersona(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/llm/persona", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPersonaGenerateReturnsPersona(t *testing.T) {
	client := &echoPersonaClient{reply: "你是嘉然，运行在 QQ 里的机器人，说话软乎乎的。"}
	rec := postPersona(personaRouterWithClient(client), `{"description":"一个爱撒娇的虚拟主播","name":"嘉然"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Persona string `json:"persona"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Persona != client.reply {
		t.Fatalf("persona = %q", resp.Persona)
	}
	// 名字和需求都要进提示词，否则生成的人设跟用户填的没关系。
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	if !strings.Contains(user, "嘉然") || !strings.Contains(user, "爱撒娇的虚拟主播") {
		t.Fatalf("user prompt = %q", user)
	}
}

func TestPersonaGenerateRewritesFromCurrentPersona(t *testing.T) {
	// 带上现有人设是「改写」而不是「重写」，否则微调一句话就丢掉已经调好的设定。
	client := &echoPersonaClient{reply: "你是嘉然。"}
	rec := postPersona(personaRouterWithClient(client), `{"description":"再毒舌一点","current":"你是嘉然，说话软乎乎的。"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	if !strings.Contains(user, "说话软乎乎的") || !strings.Contains(user, "在它的基础上") {
		t.Fatalf("user prompt = %q", user)
	}
}

func TestPersonaGenerateRejectsEmptyDescription(t *testing.T) {
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: "不该被调用"}), `{"description":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestNormalizeGeneratedPersonaStripsFencesAndQuotes(t *testing.T) {
	cases := map[string]string{
		"```\n你是嘉然。\n```":     "你是嘉然。",
		"```text\n你是嘉然。\n```": "你是嘉然。",
		"“你是嘉然。”":             "你是嘉然。",
		"「你是嘉然。」":             "你是嘉然。",
		"  你是嘉然。  ":           "你是嘉然。",
	}
	for input, want := range cases {
		if got := normalizeGeneratedPersona(input); got != want {
			t.Errorf("normalizeGeneratedPersona(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeGeneratedPersonaTruncatesOverlongOutput(t *testing.T) {
	long := strings.Repeat("你", personaGenerateMaxOutput+50)
	if got := len([]rune(normalizeGeneratedPersona(long))); got != personaGenerateMaxOutput {
		t.Fatalf("截断后长度 = %d，期望 %d", got, personaGenerateMaxOutput)
	}
}
