// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebSearchToolCallsPerplexityTinyFishAndBrave(t *testing.T) {
	var perplexityBody map[string]any
	perplexity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer pplx-test" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&perplexityBody)
		_, _ = w.Write([]byte(`{"results":[{"title":"P","url":"https://example.com/p","snippet":"perplexity hit","date":"2026-10-01"}]}`))
	}))
	defer perplexity.Close()
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "brave-test" || r.URL.Query().Get("q") != "release" || r.URL.Query().Get("count") != "3" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"B","url":"https://example.com/b","description":"<strong>brave</strong> hit","page_age":"2026-10-02"}]}}`))
	}))
	defer brave.Close()
	tinyfish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "tf-test" || r.URL.Query().Get("query") != "release" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"T1","url":"https://example.com/1","snippet":"tinyfish hit"},{"title":"T2","url":"https://example.com/2","snippet":"x"},{"title":"T3","url":"https://example.com/3","snippet":"x"},{"title":"T4","url":"https://example.com/4","snippet":"dropped"}]}`))
	}))
	defer tinyfish.Close()

	for _, tc := range []struct {
		provider WebSearchProviderConfig
		key      string
		want     string
	}{
		{WebSearchProviderConfig{Name: "perplexity", Type: "perplexity", URL: perplexity.URL, MaxResults: 3}, "pplx-test", "perplexity hit"},
		{WebSearchProviderConfig{Name: "tinyfish", Type: "tinyfish", URL: tinyfish.URL, MaxResults: 3}, "tf-test", "tinyfish hit"},
		{WebSearchProviderConfig{Name: "brave", Type: "brave", URL: brave.URL, MaxResults: 3}, "brave-test", `"brave hit"`},
	} {
		tool, err := NewWebSearchTool(WebSearchToolOptions{
			Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{tc.provider}},
			APIKeys: map[string]string{tc.provider.Name: tc.key},
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		output, err := tool.Run(context.Background(), map[string]any{"query": "release"})
		if err != nil {
			t.Fatalf("%s: %v", tc.provider.Name, err)
		}
		if !strings.Contains(output, tc.want) || strings.Contains(output, "dropped") || strings.Contains(output, tc.key) {
			t.Fatalf("%s output = %s", tc.provider.Name, output)
		}
	}
	if perplexityBody["query"] != "release" || perplexityBody["max_results"] != float64(3) {
		t.Fatalf("perplexity body = %v", perplexityBody)
	}
}

func TestWebSearchProviderDefaultsForPerplexityTinyFishAndBrave(t *testing.T) {
	config, err := NormalizeWebSearchConfig(WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "p", Type: "perplexity"}, {Name: "b", Type: "Brave"}, {Name: "t", Type: "tinyfish"}}})
	if err != nil {
		t.Fatal(err)
	}
	if config.Providers[0].URL != "https://api.perplexity.ai/search" || config.Providers[1].URL != "https://api.search.brave.com/res/v1/web/search" || config.Providers[2].URL != "https://api.search.tinyfish.ai" {
		t.Fatalf("providers = %+v", config.Providers)
	}
}
