// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 保存配置时没填模型会顺手拉一次模型列表。供应商不回话时这一步以前只跟着浏览器
// 连接走，页面一直转圈；现在和 /api/llm/models 同一个上限。
func TestLLMConfigSaveModelListTimesOutSlowProvider(t *testing.T) {
	originalTimeout := llmModelListTimeout
	llmModelListTimeout = 50 * time.Millisecond
	t.Cleanup(func() { llmModelListTimeout = originalTimeout })

	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	handler.SetModelListFactory(func(ctx context.Context, _ llm.ProviderConfig) ([]llm.ModelInfo, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	router := testRouter(handler)

	body := []byte(`{"name":"Gemini","provider":"gemini","api_key":"valid-key-123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("save waited %s for a hung model list", elapsed)
	}
}

// 新版供应商页的「拉取模型」直连供应商的 /models。服务端收了请求却不回，
// 以前要等到浏览器放弃。
func TestLLMProviderModelsTimesOutHungEndpoint(t *testing.T) {
	originalTimeout := llmModelListTimeout
	llmModelListTimeout = 100 * time.Millisecond
	t.Cleanup(func() { llmModelListTimeout = originalTimeout })

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "valid-key-123",
		BaseURL:  server.URL + "/v1",
		Model:    "saved-model",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)
	providerID := store.Profiles().Profiles[0].ID

	req := httptest.NewRequest(http.MethodPost, "/api/llm/providers/models", bytes.NewReader([]byte(`{"providerId":"`+providerID+`"}`)))
	rec := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("provider model list waited %s for a hung endpoint", elapsed)
	}
}
