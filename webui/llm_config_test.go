// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// TestLLMConfigHandlerGetAndPost 验证对应功能场景。
func TestLLMConfigHandlerGetAndPost(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	postBody := []byte(`{"id":"` + store.Profiles().ActiveID + `","name":"主配置","group":"chat","description":"主力 OpenAI 配置","provider":"openai_compatible","api_key":"new-key-123","models":[{"id":"gpt-test"},{"id":"gpt-vision"}],"model":"gpt-test","image_model":"gpt-image-1-mini","user_agent":"codex-test/1.0","headers":{"X-Relay":"earlyso"},"temperature":0.5,"max_output_tokens":128,"timeout_ms":5000}`)
	postReq := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(postBody))
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body = %s", postRec.Code, postRec.Body.String())
	}
	current := store.Current()
	if current.Provider != llm.ProviderOpenAICompatible || current.APIKey != "new-key-123" || current.Model != "gpt-test" || len(current.Models) != 2 || current.Models[1].ID != "gpt-vision" || current.ImageModel != "gpt-image-1-mini" || current.UserAgent != "codex-test/1.0" || current.Headers["X-Relay"] != "earlyso" || current.MaxOutputTokens != 128 {
		t.Fatalf("current config = %#v", current)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/llm/config", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	var payload llmConfigPayload
	if err := json.NewDecoder(getRec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload.Name != "主配置" || payload.Group != "chat" || payload.Description != "主力 OpenAI 配置" || payload.UpdatedAt == "" || len(payload.Models) != 2 || payload.Models[1].ID != "gpt-vision" || payload.TimeoutMS != 5000 || payload.ImageModel != "gpt-image-1-mini" || payload.UserAgent != "codex-test/1.0" || payload.Headers["X-Relay"] != "earlyso" || payload.MaxOutputTokens != 128 {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.APIKey != "" || !payload.APIKeyConfigured || payload.APIKeyPreview != "new…123" {
		t.Fatalf("api key leaked or configured flag wrong: %#v", payload)
	}
	if len(payload.Profiles) != 1 {
		t.Fatalf("profiles = %#v", payload.Profiles)
	}
}

func TestMaskLLMAPIKeyKeepsMiddleHidden(t *testing.T) {
	tests := map[string]string{
		"sk-1234567890abcdef": "sk-12…cdef",
		"12345678":            "123…678",
		"短密钥":                 "短…钥",
		"x":                   "••••",
		"":                    "",
	}
	for input, want := range tests {
		if got := maskLLMAPIKey(input); got != want {
			t.Fatalf("maskLLMAPIKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLLMAdvancedConfigRoundTripAndCompactEditorPreservation(t *testing.T) {
	advanced := llm.ProviderConfig{
		Provider:            llm.ProviderOpenAICompatible,
		APIKey:              "secret-key",
		BaseURL:             "https://chat.example.test/v1",
		APIFormat:           llm.APIFormatChatCompletions,
		Model:               "gpt-test",
		ImageModel:          "gpt-image-2",
		ImageBaseURL:        "https://image.example.test/v1",
		ImageOrigin:         "203.0.113.10:443",
		ImageTimeout:        10 * time.Minute,
		Headers:             map[string]string{"X-Relay": "preserve-me"},
		ReasoningEffort:     "high",
		ContextWindowTokens: 200000,
		MaxContextTokens:    12000,
		Timeout:             45 * time.Second,
	}
	payload := payloadFromConfig(advanced)
	roundTrip := configFromPayload(payload)
	if roundTrip.ImageBaseURL != advanced.ImageBaseURL || roundTrip.ImageOrigin != advanced.ImageOrigin || roundTrip.ImageTimeout != advanced.ImageTimeout || roundTrip.APIFormat != advanced.APIFormat || roundTrip.ReasoningEffort != "high" || roundTrip.ContextWindowTokens != 200000 || roundTrip.MaxContextTokens != 12000 || roundTrip.Timeout != advanced.Timeout {
		t.Fatalf("advanced round trip = %#v", roundTrip)
	}

	store := NewMemoryLLMProfileStore(advanced)
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)
	body := []byte(`{"id":"` + store.Profiles().ActiveID + `","name":"主配置","provider":"openai_compatible","api_style":"chat_completions","model":"gpt-test","base_url":"https://chat.example.test/v1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := store.Current()
	if got.ImageModel != advanced.ImageModel || got.ImageBaseURL != advanced.ImageBaseURL || got.ImageOrigin != advanced.ImageOrigin || got.ImageTimeout != advanced.ImageTimeout || got.Headers["X-Relay"] != "preserve-me" || got.ReasoningEffort != "high" || got.ContextWindowTokens != 200000 || got.MaxContextTokens != 12000 || got.Timeout != advanced.Timeout {
		t.Fatalf("compact editor dropped advanced settings: %#v", got)
	}
}

// TestLLMConfigHandlerGetCanIncludeSecrets 验证对应功能场景。
func TestLLMConfigHandlerGetCanIncludeSecrets(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "secret-key",
		Model:    "gp5.5",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/llm/config?include_secrets=true", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload llmConfigPayload
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload.APIKey != "secret-key" || len(payload.Profiles) != 1 || payload.Profiles[0].APIKey != "secret-key" {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestLLMConfigHandlerExportIncludesAPIKeys 验证对应功能场景。
func TestLLMConfigHandlerExportIncludesAPIKeys(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "secret-key",
		Model:    "gp5.5",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/llm/config/export", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload llmConfigPayload
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload.APIKey != "secret-key" {
		t.Fatalf("api key = %q", payload.APIKey)
	}
	if len(payload.Profiles) != 1 || payload.Profiles[0].APIKey != "secret-key" {
		t.Fatalf("profiles = %#v", payload.Profiles)
	}
}

// TestLLMConfigHandlerCreatesNamedProfile 验证对应功能场景。
func TestLLMConfigHandlerCreatesNamedProfile(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	body := []byte(`{"name":"备用 Key","description":"备用 Anthropic 配置","provider":"anthropic","api_key":"valid-key-123","model":"claude-sonnet-4-5","timeout_ms":5000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	profiles := store.Profiles()
	if len(profiles.Profiles) != 2 {
		t.Fatalf("profiles = %#v", profiles)
	}
	current, ok := profiles.Current()
	if !ok || current.Name != "备用 Key" || current.Description != "备用 Anthropic 配置" || current.UpdatedAt.IsZero() || current.Config.Provider != llm.ProviderAnthropic {
		t.Fatalf("current profile = %#v", current)
	}
}

// TestLLMConfigHandlerAppliesProviderDefaults 验证对应功能场景。
func TestLLMConfigHandlerAppliesProviderDefaults(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	body := []byte(`{"name":"Gemini","provider":"gemini","api_key":"valid-key-123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	current := store.Current()
	if current.Provider != llm.ProviderGemini || current.Model != llm.DefaultGeminiModel || current.ImageModel != llm.DefaultImageModel(llm.ProviderGemini) {
		t.Fatalf("current = %#v", current)
	}
}

// TestLLMConfigHandlerActivateProfile 验证对应功能场景。
func TestLLMConfigHandlerActivateProfile(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	profiles := store.Profiles()
	profiles.Profiles = append(profiles.Profiles, llm.Profile{
		ID:   "secondary",
		Name: "备用",
		Config: llm.ProviderConfig{
			Provider: llm.ProviderAnthropic,
			APIKey:   "anthropic-key",
			Model:    "claude-sonnet-4-5",
		},
	})
	store.SaveProfiles(profiles)
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/config/activate", bytes.NewReader([]byte(`{"id":"secondary"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := store.Current(); got.Provider != llm.ProviderAnthropic || got.Model != "claude-sonnet-4-5" {
		t.Fatalf("current = %#v", got)
	}
}

// TestLLMConfigHandlerDeleteProfile 验证对应功能场景。
func TestLLMConfigHandlerDeleteProfile(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	profiles := store.Profiles()
	profiles.Profiles = append(profiles.Profiles, llm.Profile{
		ID:   "secondary",
		Name: "备用",
		Config: llm.ProviderConfig{
			Provider: llm.ProviderAnthropic,
			APIKey:   "anthropic-key",
			Model:    "claude-sonnet-4-5",
		},
	})
	store.SaveProfiles(profiles)
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/config/delete", bytes.NewReader([]byte(`{"id":"secondary"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(store.Profiles().Profiles) != 1 {
		t.Fatalf("profiles = %#v", store.Profiles())
	}
}

// TestLLMConfigHandlerCloneProfile 验证对应功能场景。
func TestLLMConfigHandlerCloneProfile(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/config/clone", bytes.NewReader([]byte(`{"id":"`+store.Profiles().ActiveID+`"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	profiles := store.Profiles()
	if len(profiles.Profiles) != 2 {
		t.Fatalf("profiles = %#v", profiles)
	}
	current, _ := profiles.Current()
	if current.Name == "默认配置" || current.Config.APIKey != "old-key" {
		t.Fatalf("current = %#v", current)
	}
	if current.Config.APIFormat != llm.APIFormatResponses {
		t.Fatalf("cloned APIFormat = %q, want %q", current.Config.APIFormat, llm.APIFormatResponses)
	}
}

// TestLLMConfigHandlerImportProfiles 验证对应功能场景。
func TestLLMConfigHandlerImportProfiles(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "old-key",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	body := []byte(`{
	  "active_profile_id":"b",
	  "profiles":[
	    {"id":"a","name":"主配置","provider":"openai_compatible","api_key":"key-a","model":"gp5.5","timeout_ms":30000},
	    {"id":"b","name":"备用配置","provider":"anthropic","api_key":"key-b","model":"claude-sonnet-4-5","timeout_ms":30000}
	  ]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config/import", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	current := store.Current()
	if current.Provider != llm.ProviderAnthropic || current.APIKey != "key-b" {
		t.Fatalf("current = %#v", current)
	}
	profiles := store.Profiles()
	for _, profile := range profiles.Profiles {
		switch profile.ID {
		case "a":
			if profile.Config.APIFormat != llm.APIFormatResponses {
				t.Fatalf("imported OpenAI-compatible APIFormat = %q, want %q", profile.Config.APIFormat, llm.APIFormatResponses)
			}
		case "b":
			if profile.Config.APIFormat != "" {
				t.Fatalf("imported Anthropic APIFormat = %q, want empty", profile.Config.APIFormat)
			}
		}
	}
}

// TestLLMConfigHandlerRejectsInvalidConfig 验证对应功能场景。
func TestLLMConfigHandlerRejectsInvalidConfig(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader([]byte(`{"provider":"anthropic","model":"claude-sonnet-4-5"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestLLMConfigHandlerRejectsUnsupportedProvider 验证对应功能场景。
func TestLLMConfigHandlerRejectsUnsupportedProvider(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader([]byte(`{"provider":"unknown","api_key":"valid-key","model":"x"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestLLMConfigHandlerRejectsInvalidBaseURL 验证对应功能场景。
func TestLLMConfigHandlerRejectsInvalidBaseURL(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader([]byte(`{"provider":"openai_compatible","api_key":"valid-key","base_url":"api.example.com/v1","model":"gp5.5"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestLLMConfigHandlerRejectsShortAPIKey 验证对应功能场景。
func TestLLMConfigHandlerRejectsShortAPIKey(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	body := []byte(`{"provider":"anthropic","api_key":"short","model":"claude-sonnet-4-5"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestLLMConfigHandlerKeepsExistingAPIKeyWhenOmitted 验证对应功能场景。
func TestLLMConfigHandlerKeepsExistingAPIKeyWhenOmitted(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderAnthropic,
		APIKey:   "existing-key",
		Model:    "claude-sonnet-4-5",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	body := []byte(`{"id":"` + store.Profiles().ActiveID + `","provider":"anthropic","model":"claude-opus-4-6","max_output_tokens":256}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := store.Current(); got.APIKey != "existing-key" || got.Model != "claude-opus-4-6" {
		t.Fatalf("stored config = %#v", got)
	}
}

// TestLLMConfigHandlerTestEndpoint 验证对应功能场景。
func TestLLMConfigHandlerTestEndpoint(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "test-key",
		Model:    "gpt-test",
	})

	handler := NewLLMConfigHandlerWithFactory(store, func(cfg llm.ProviderConfig) (llm.LLMClient, error) {
		if cfg.Model != "gpt-test" {
			t.Fatalf("factory config = %#v", cfg)
		}
		return fakeLLMClient{}, nil
	})
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader([]byte(`{"message":"hello"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp llm.GenerateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if resp.Text != "ok: hello" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

// TestLLMConfigHandlerTestEndpointUsesPayloadConfig 验证对应功能场景。
func TestLLMConfigHandlerTestEndpointUsesPayloadConfig(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "saved-key",
		Model:    "saved-model",
	})
	handler := NewLLMConfigHandlerWithFactory(store, func(cfg llm.ProviderConfig) (llm.LLMClient, error) {
		if cfg.Model != "draft-model" || cfg.BaseURL != "https://draft.example/v1" {
			t.Fatalf("factory config = %#v", cfg)
		}
		if cfg.APIKey != "saved-key" {
			t.Fatalf("api key = %q", cfg.APIKey)
		}
		return fakeLLMClient{}, nil
	})
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader([]byte(`{"message":"hello","id":"`+store.Profiles().ActiveID+`","provider":"openai_compatible","base_url":"https://draft.example/v1","model":"draft-model"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLLMConfigHandlerTestEndpointUsesImageGenerationForImageGroup(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "saved-key",
		Model:      "saved-text-model",
		ImageModel: "saved-image-model",
	})
	var gotRequest llm.ImageGenerateRequest
	handler := NewLLMConfigHandlerWithFactory(store, func(cfg llm.ProviderConfig) (llm.LLMClient, error) {
		if cfg.APIKey != "saved-key" || cfg.ImageModel != "selected-image-model" {
			t.Fatalf("factory config = %#v", cfg)
		}
		return fakeImageLLMClient{request: &gotRequest}, nil
	})
	router := testRouter(handler)

	body := []byte(`{"message":"生成一只小猫","id":"` + store.Profiles().ActiveID + `","group":"image","provider":"openai_compatible","model":"selected-image-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotRequest.Prompt != "生成一只小猫" || gotRequest.Model != "selected-image-model" || gotRequest.N != 1 {
		t.Fatalf("image request = %#v", gotRequest)
	}
	var resp llm.ImageGenerateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if resp.Model != "selected-image-model" || len(resp.Images) != 1 || resp.Images[0] != "data:image/png;base64,YWJjZA==" {
		t.Fatalf("image response = %#v", resp)
	}
}

// TestLLMConfigHandlerModelsEndpointKeepsExistingAPIKey 验证对应功能场景。
func TestLLMConfigHandlerModelsEndpointKeepsExistingAPIKey(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "existing-key",
		BaseURL:  "https://saved.example/v1",
		Model:    "old-model",
	})

	handler := NewLLMConfigHandler(store)
	handler.SetModelListFactory(func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		if cfg.APIKey != "existing-key" || cfg.BaseURL != "https://new.example/v1" || cfg.Model != "gp5.5" {
			t.Fatalf("model list config = %#v", cfg)
		}
		return []llm.ModelInfo{{ID: "gp5.5"}, {ID: "gpt-4o-mini"}}, nil
	})
	router := testRouter(handler)

	body := []byte(`{"id":"` + store.Profiles().ActiveID + `","provider":"openai_compatible","base_url":"https://new.example/v1","model":"gp5.5"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload llmModelsPayload
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(payload.Models) != 2 || payload.Models[0].ID != "gp5.5" {
		t.Fatalf("models payload = %#v", payload)
	}
}

func TestLLMConfigHandlerModelsTimesOutSlowProvider(t *testing.T) {
	originalTimeout := llmModelListTimeout
	llmModelListTimeout = 50 * time.Millisecond
	t.Cleanup(func() { llmModelListTimeout = originalTimeout })

	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "existing-key",
		Model:    "saved-model",
	})
	handler := NewLLMConfigHandler(store)
	handler.SetModelListFactory(func(ctx context.Context, _ llm.ProviderConfig) ([]llm.ModelInfo, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", bytes.NewReader([]byte(`{"provider":"openai_compatible","model":"saved-model"}`)))
	rec := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("model list timeout took %s", elapsed)
	}
}

func TestLLMConfigHandlerNewDraftDoesNotReuseActiveAPIKey(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "active-secret-key",
		BaseURL:  "https://saved.example/v1",
		Model:    "saved-model",
	})
	handler := NewLLMConfigHandler(store)
	handler.SetModelListFactory(func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		if cfg.APIKey != "" {
			t.Fatalf("new draft reused active api key: %q", cfg.APIKey)
		}
		return nil, llm.ErrMissingAPIKey
	})
	router := testRouter(handler)

	body := []byte(`{"provider":"openai_compatible","base_url":"https://new.example/v1","model":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLLMConfigHandlerSaveResolvesEmptyModelFromList(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "existing-key",
		BaseURL:  "https://saved.example/v1",
		Model:    "old-model",
	})
	handler := NewLLMConfigHandler(store)
	handler.SetModelListFactory(func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		if cfg.APIKey != "new-api-key" || cfg.BaseURL != "https://new.example/v1" {
			t.Fatalf("model list config = %#v", cfg)
		}
		return []llm.ModelInfo{{ID: "auto-model"}, {ID: "other-model"}}, nil
	})
	router := testRouter(handler)

	body := []byte(`{"provider":"openai_compatible","api_style":"chat_completions","api_key":"new-api-key","base_url":"https://new.example/v1","model":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if current := store.Current(); current.Model != "auto-model" || len(current.Models) != 2 || current.Models[1].ID != "other-model" {
		t.Fatalf("saved config = %#v, want auto-model and complete model list", current)
	}
}

func TestLLMConfigHandlerLegacySavePreservesModels(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "existing-key",
		BaseURL:  "https://saved.example/v1",
		Models:   []llm.ModelInfo{{ID: "model-a"}, {ID: "model-b"}},
		Model:    "model-a",
	})
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)

	body := []byte(`{"id":"` + store.Profiles().ActiveID + `","provider":"openai_compatible","base_url":"https://saved.example/v1","model":"model-b"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	current := store.Current()
	if current.Model != "model-b" || len(current.Models) != 2 {
		t.Fatalf("saved config = %#v", current)
	}
}

type fakeLLMClient struct{}

// Generate 调用当前模型 provider 生成回复。
func (fakeLLMClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{
		Provider: llm.ProviderOpenAICompatible,
		Model:    req.Model,
		Text:     "ok: " + req.Messages[0].Content,
	}, nil
}

type fakeImageLLMClient struct {
	request *llm.ImageGenerateRequest
}

func (fakeImageLLMClient) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return nil, fmt.Errorf("text generation should not be called for an image test")
}

func (client fakeImageLLMClient) GenerateImage(_ context.Context, req llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, error) {
	if client.request != nil {
		*client.request = req
	}
	return &llm.ImageGenerateResponse{
		Provider: llm.ProviderOpenAICompatible,
		Model:    req.Model,
		Images:   []string{"data:image/png;base64,YWJjZA=="},
	}, nil
}

// testRouter 封装当前模块的 testRouter 逻辑。
func testRouter(handler *LLMConfigHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}

// 「模型默认填了 400k」的由来：WithDefaults 把推断出来的窗口写进配置对象，回显
// 时就长得像用户自己填的；用户随手一保存，这个猜测就变成了真正的设置。
// 回显必须只报用户填过的值，推断结果单独放在只读字段里。
func TestLLMPayloadReportsOverridesSeparatelyFromEffectiveWindow(t *testing.T) {
	payload := payloadFromConfig(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "sk-test",
		Model:    "claude-sonnet-4-5",
	})
	if payload.ContextWindowTokens != nil || payload.MaxContextTokens != nil {
		t.Fatalf("推断值被当成用户设置回显了: %v/%v", payload.ContextWindowTokens, payload.MaxContextTokens)
	}
	if payload.EffectiveContextWindowTokens != 200000 || payload.EffectiveMaxContextTokens != 200000 {
		t.Fatalf("effective = %d/%d", payload.EffectiveContextWindowTokens, payload.EffectiveMaxContextTokens)
	}
	if payload.ContextWindowSource != llm.ContextWindowSourceInferred {
		t.Fatalf("source = %q", payload.ContextWindowSource)
	}

	// 模型清单里有这个模型时按清单走，而且换模型会跟着变——窗口是模型的属性。
	withList := llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "sk-test",
		Model:    "house-model",
		Models: []llm.ModelInfo{
			{ID: "house-model", ContextWindowTokens: 65536},
			{ID: "house-model-mini", ContextWindowTokens: 8192},
		},
	}
	listed := payloadFromConfig(withList)
	if listed.EffectiveContextWindowTokens != 65536 || listed.ContextWindowSource != llm.ContextWindowSourceModelList {
		t.Fatalf("model list window = %d source %q", listed.EffectiveContextWindowTokens, listed.ContextWindowSource)
	}
	withList.Model = "house-model-mini"
	if switched := payloadFromConfig(withList); switched.EffectiveContextWindowTokens != 8192 {
		t.Fatalf("换模型后窗口没跟着变: %d", switched.EffectiveContextWindowTokens)
	}

	// 用户填过的值原样回显，并标明来源是用户。
	explicit := payloadFromConfig(llm.ProviderConfig{
		Provider:            llm.ProviderOpenAICompatible,
		APIKey:              "sk-test",
		Model:               "claude-sonnet-4-5",
		ContextWindowTokens: 32768,
	})
	if explicit.ContextWindowTokens == nil || *explicit.ContextWindowTokens != 32768 {
		t.Fatalf("override = %v", explicit.ContextWindowTokens)
	}
	if explicit.ContextWindowSource != llm.ContextWindowSourceUser {
		t.Fatalf("source = %q", explicit.ContextWindowSource)
	}
}

// 清空输入框要能真的清掉。以前 payload 是 int64，「清空」和「没提交这个字段」都
// 是 0，于是填过的窗口再也删不掉。
func TestLLMPayloadDistinguishesClearedFromUnsubmitted(t *testing.T) {
	existing := llm.ProviderConfig{
		Provider:            llm.ProviderOpenAICompatible,
		APIKey:              "sk-test",
		Model:               "claude-sonnet-4-5",
		ContextWindowTokens: 32768,
		MaxContextTokens:    16384,
	}
	cleared := llmConfigPayload{Provider: llm.ProviderOpenAICompatible, Model: "claude-sonnet-4-5"}
	zero := int64(0)
	cleared.ContextWindowTokens = &zero
	cleared.MaxContextTokens = &zero
	merged := mergeUnsubmittedLLMConfig(cleared, configFromPayload(cleared), existing)
	if merged.ContextWindowTokens != 0 || merged.MaxContextTokens != 0 {
		t.Fatalf("清空没生效: %d/%d", merged.ContextWindowTokens, merged.MaxContextTokens)
	}

	untouched := llmConfigPayload{Provider: llm.ProviderOpenAICompatible, Model: "claude-sonnet-4-5"}
	kept := mergeUnsubmittedLLMConfig(untouched, configFromPayload(untouched), existing)
	if kept.ContextWindowTokens != 32768 || kept.MaxContextTokens != 16384 {
		t.Fatalf("没提交的字段应当保留旧值: %d/%d", kept.ContextWindowTokens, kept.MaxContextTokens)
	}
}
