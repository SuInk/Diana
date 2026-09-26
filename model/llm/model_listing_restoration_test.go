// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleHasNoUniversalModelPreset(t *testing.T) {
	if got := DefaultModel(ProviderOpenAICompatible); got != "" {
		t.Fatalf("default model = %q, want explicit selection", got)
	}
	if presets := ModelPresets(ProviderOpenAICompatible); len(presets) != 0 {
		t.Fatalf("presets = %#v, want backend-provided models", presets)
	}
}

func TestNoProviderExposesBuiltInModelsAsSynchronizedList(t *testing.T) {
	for _, provider := range []Provider{ProviderOpenAICompatible, ProviderGemini, ProviderAnthropic} {
		if presets := ModelPresets(provider); len(presets) != 0 {
			t.Fatalf("provider %q presets = %#v", provider, presets)
		}
	}
}

func TestOpenAICompatibleModelListingFallsBackAfterDecodeError(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/model" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>proxy landing page</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL,
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models = %#v", models)
	}
	if len(paths) != 2 || paths[0] != "/model" || paths[1] != "/models" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestOpenAICompatibleModelListErrorIncludesRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer server.Close()

	_, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL,
	}, WithHTTPClient(server.Client()))
	if err == nil {
		t.Fatal("ListModels() error = nil")
	}
	for _, want := range []string{"GET " + server.URL + "/model", "状态：401", "application/json", "invalid token"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err, want)
		}
	}
}

func TestGeminiModelListingUsesLivePagedEndpoint(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Fatalf("x-goog-api-key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "next-1" {
			_, _ = w.Write([]byte(`{"models":[{"name":"models/model-b","displayName":"Model B","inputTokenLimit":2000,"outputTokenLimit":200}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/model-a","displayName":"Model A","inputTokenLimit":1000,"outputTokenLimit":100}],"nextPageToken":"next-1"}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{Provider: ProviderGemini, APIKey: "test-key", BaseURL: server.URL + "/v1beta/models"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" || models[0].ContextWindowTokens != 1000 || models[1].MaxOutputTokens != 200 {
		t.Fatalf("models = %#v", models)
	}
	if len(paths) != 2 || !strings.HasPrefix(paths[0], "/v1beta/models?pageSize=1000") || !strings.Contains(paths[1], "pageToken=next-1") {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestGeminiModelListingSurfacesUpstreamErrorInsteadOfPresets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"accounts exhausted"}}`))
	}))
	defer server.Close()

	_, err := ListModels(context.Background(), ProviderConfig{Provider: ProviderGemini, APIKey: "test-key", BaseURL: server.URL + "/v1beta"}, WithHTTPClient(server.Client()))
	if err == nil || !strings.Contains(err.Error(), "状态：429") || !strings.Contains(err.Error(), "accounts exhausted") {
		t.Fatalf("error = %v", err)
	}
	if presets := ModelPresets(ProviderGemini); len(presets) != 0 {
		t.Fatalf("Gemini presets must not masquerade as synchronized models: %#v", presets)
	}
}

func TestAnthropicModelListingReadsV1ModelsAndFollowsPages(t *testing.T) {
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-sonnet-5","display_name":"Claude Sonnet 5","created_at":"2026-01-01T00:00:00Z"}],"has_more":true,"first_id":"claude-sonnet-5","last_id":"claude-sonnet-5"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5"}],"has_more":false,"last_id":"claude-haiku-4-5"}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderAnthropic,
		APIKey:   "sk-test",
		BaseURL:  server.URL,
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "claude-sonnet-5" || models[0].Name != "Claude Sonnet 5" || models[1].ID != "claude-haiku-4-5" {
		t.Fatalf("models = %#v", models)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	first := requests[0]
	if first.URL.Path != "/v1/models" || first.Header.Get("x-api-key") != "sk-test" || first.Header.Get("anthropic-version") == "" {
		t.Fatalf("first request = %s %v", first.URL, first.Header)
	}
	if got := requests[1].URL.Query().Get("after_id"); got != "claude-sonnet-5" {
		t.Fatalf("after_id = %q", got)
	}
}

func TestAnthropicModelListingAcceptsOpenAIShapedRelayResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"claude-opus-4-6","object":"model","owned_by":"relay"}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{Provider: ProviderAnthropic, APIKey: "sk-test", BaseURL: server.URL}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "claude-opus-4-6" || models[0].OwnedBy != "relay" {
		t.Fatalf("models = %#v", models)
	}
}

func TestAnthropicModelListErrorIncludesRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer server.Close()

	_, err := ListModels(context.Background(), ProviderConfig{Provider: ProviderAnthropic, APIKey: "sk-test", BaseURL: server.URL}, WithHTTPClient(server.Client()))
	if err == nil {
		t.Fatal("ListModels() error = nil")
	}
	for _, want := range []string{"GET " + server.URL + "/v1/models", "状态：401", "invalid x-api-key"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err, want)
		}
	}
}
