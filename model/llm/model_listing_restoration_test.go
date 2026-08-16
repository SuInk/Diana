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
