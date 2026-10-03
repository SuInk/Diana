package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestProvidersEndpointReturnsMigratedModelsWithoutAPIKeys(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "super-secret-key", BaseURL: "https://gateway.example/v1", Model: "chat-model"})
	set := store.Profiles()
	set.Profiles[0].Config.Models = []llm.ModelInfo{{ID: "chat-model"}, {ID: "vision-model"}}
	store.SaveProfiles(set)
	handler := NewLLMConfigHandler(store)
	router := testRouter(handler)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/llm/providers", nil)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !containsAll(body, `"providers"`, `"models"`, "chat-model", "vision-model") {
		t.Fatalf("body=%s", body)
	}
	if containsAll(body, "super-secret-key", "api_key") {
		t.Fatalf("response leaked secret: %s", body)
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}

func TestProviderRequestLimitsPayloadRoundTripAndLegacyPreservation(t *testing.T) {
	existing := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, MaxConcurrency: 2, MaxRPS: 0.5}
	payload := payloadFromConfig(existing)
	if payload.MaxConcurrency == nil || *payload.MaxConcurrency != 2 || payload.MaxRPS == nil || *payload.MaxRPS != 0.5 {
		t.Fatalf("payload=%+v", payload)
	}
	cfg := configFromPayload(payload)
	if cfg.MaxConcurrency != 2 || cfg.MaxRPS != 0.5 {
		t.Fatalf("round trip=%+v", cfg)
	}
	legacy := llmConfigPayload{Provider: llm.ProviderOpenAICompatible}
	cfg = mergeUnsubmittedLLMConfig(legacy, configFromPayload(legacy), existing)
	if cfg.MaxConcurrency != 2 || cfg.MaxRPS != 0.5 {
		t.Fatalf("legacy client erased limits: %+v", cfg)
	}
	zeroInt, zeroFloat := 0, 0.0
	legacy.MaxConcurrency = &zeroInt
	legacy.MaxRPS = &zeroFloat
	cfg = mergeUnsubmittedLLMConfig(legacy, configFromPayload(legacy), existing)
	if cfg.MaxConcurrency != 0 || cfg.MaxRPS != 0 {
		t.Fatalf("explicit unlimited was ignored: %+v", cfg)
	}
}
