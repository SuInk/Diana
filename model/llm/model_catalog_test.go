// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestModelInfoFromPayloadReadsContextLimits(t *testing.T) {
	model := modelInfoFromPayload(map[string]any{
		"id": "test-model",
		"limit": map[string]any{
			"context": float64(200000),
			"input":   float64(180000),
			"output":  float64(20000),
		},
	})
	if model.ContextWindowTokens != 200000 || model.MaxInputTokens != 180000 || model.MaxOutputTokens != 20000 {
		t.Fatalf("model = %#v", model)
	}
}

func TestModelInfoFromPayloadReadsModalities(t *testing.T) {
	model := modelInfoFromPayload(map[string]any{
		"id": "vision-model",
		"modalities": map[string]any{
			"input":  []any{"TEXT", "image", "image"},
			"output": []any{"text"},
		},
	})
	if len(model.InputModalities) != 2 || model.InputModalities[0] != "text" || model.InputModalities[1] != "image" {
		t.Fatalf("input modalities = %#v", model.InputModalities)
	}
	if len(model.OutputModalities) != 1 || model.OutputModalities[0] != "text" {
		t.Fatalf("output modalities = %#v", model.OutputModalities)
	}

	model = modelInfoFromPayload(map[string]any{
		"id": "image-model",
		"architecture": map[string]any{
			"input_modalities":  []any{"text", "image"},
			"output_modalities": []any{"image"},
		},
	})
	if len(model.OutputModalities) != 1 || model.OutputModalities[0] != "image" {
		t.Fatalf("architecture modalities = %#v", model)
	}
}

func TestModelsDevCatalogEnrichesOpenCodeGoAndCaches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"opencode-go":{"models":{"deepseek-v4-flash":{"name":"DeepSeek V4 Flash","modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":1000000,"input":900000,"output":384000}}}}}`))
	}))
	defer server.Close()

	catalog := newModelsDevCatalog(server.Client(), server.URL)
	cfg := ProviderConfig{
		Provider: ProviderOpenAICompatible,
		BaseURL:  "https://opencode.ai/zen/go/v1",
	}
	models := []ModelInfo{{ID: "deepseek-v4-flash", OwnedBy: "opencode"}}
	for attempt := 0; attempt < 2; attempt++ {
		got := catalog.Enrich(context.Background(), cfg, models)
		if len(got) != 1 || got[0].ContextWindowTokens != 1000000 || got[0].MaxInputTokens != 900000 || got[0].MaxOutputTokens != 384000 || len(got[0].InputModalities) != 2 || len(got[0].OutputModalities) != 1 || got[0].OutputModalities[0] != "text" {
			t.Fatalf("models = %#v", got)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("catalog requests = %d, want 1", got)
	}
}

func TestModelsDevProviderCandidatesTreatsEmptyOpenAIBaseURLAsOfficial(t *testing.T) {
	got := modelsDevProviderCandidates(ProviderConfig{Provider: ProviderOpenAICompatible})
	if len(got) != 1 || got[0] != "openai" {
		t.Fatalf("provider candidates = %#v", got)
	}
}

func TestModelsDevProviderCandidatesRecognizesKnownCompatibleEndpoints(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{baseURL: "https://api.deepseek.com", want: "deepseek"},
		{baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", want: "google"},
		{baseURL: "https://openrouter.ai/api/v1", want: "openrouter"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := modelsDevProviderCandidates(ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: tt.baseURL})
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("provider candidates = %#v", got)
			}
		})
	}
}

func TestModelsDevCatalogDoesNotGuessCustomGatewayProvider(t *testing.T) {
	catalog := newModelsDevCatalog(http.DefaultClient, "https://models.invalid/api.json")
	models := []ModelInfo{{ID: "gpt-5.6-sol"}}
	got := catalog.Enrich(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		BaseURL:  "https://custom.example/v1",
	}, models)
	if len(got) != 1 || got[0].ContextWindowTokens != 0 {
		t.Fatalf("models = %#v", got)
	}
}
