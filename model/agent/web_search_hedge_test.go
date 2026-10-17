// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func hedgeTestServer(t *testing.T, delay time.Duration, status int, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		if status != http.StatusOK {
			http.Error(w, "unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Hit","url":"https://example.org/` + r.Host + `","content":"found"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func runHedgeSearch(t *testing.T, primary, fallback *httptest.Server) (webSearchResult, time.Duration) {
	t.Helper()
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config:  WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "primary", Type: "tavily", URL: primary.URL, TimeoutMS: 5000}, {Name: "fallback", Type: "tavily", URL: fallback.URL, TimeoutMS: 5000}}},
		APIKeys: map[string]string{"primary": "k", "fallback": "k"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	output, err := tool.Run(context.Background(), map[string]any{"query": "diana"})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	return result, time.Since(started)
}

func withHedgeDelay(t *testing.T, delay time.Duration) {
	previous := webSearchHedgeDelay
	webSearchHedgeDelay = delay
	t.Cleanup(func() { webSearchHedgeDelay = previous })
}

func TestWebSearchHedgesSlowPrimaryWithNextSource(t *testing.T) {
	withHedgeDelay(t, 50*time.Millisecond)
	var primaryCalls, fallbackCalls atomic.Int32
	primary := hedgeTestServer(t, 2*time.Second, http.StatusOK, &primaryCalls)
	fallback := hedgeTestServer(t, 20*time.Millisecond, http.StatusOK, &fallbackCalls)
	result, elapsed := runHedgeSearch(t, primary, fallback)
	if result.Status != "ok" || result.Provider != "fallback" || elapsed > time.Second {
		t.Fatalf("status=%s provider=%s elapsed=%s", result.Status, result.Provider, elapsed)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestWebSearchDoesNotHedgeFastPrimary(t *testing.T) {
	withHedgeDelay(t, 500*time.Millisecond)
	var primaryCalls, fallbackCalls atomic.Int32
	primary := hedgeTestServer(t, 20*time.Millisecond, http.StatusOK, &primaryCalls)
	fallback := hedgeTestServer(t, 0, http.StatusOK, &fallbackCalls)
	result, _ := runHedgeSearch(t, primary, fallback)
	if result.Provider != "primary" || fallbackCalls.Load() != 0 {
		t.Fatalf("provider=%s fallback calls=%d", result.Provider, fallbackCalls.Load())
	}
}

func TestWebSearchFailedPrimaryStartsFallbackImmediately(t *testing.T) {
	withHedgeDelay(t, 10*time.Second)
	var primaryCalls, fallbackCalls atomic.Int32
	primary := hedgeTestServer(t, 0, http.StatusServiceUnavailable, &primaryCalls)
	fallback := hedgeTestServer(t, 20*time.Millisecond, http.StatusOK, &fallbackCalls)
	result, elapsed := runHedgeSearch(t, primary, fallback)
	if result.Provider != "fallback" || elapsed > 2*time.Second || !result.FallbackUsed {
		t.Fatalf("provider=%s elapsed=%s fallback=%v", result.Provider, elapsed, result.FallbackUsed)
	}
}

func TestWebSearchPanickedProviderStillFallsBack(t *testing.T) {
	withHedgeDelay(t, 10*time.Second)
	var calls atomic.Int32
	fallback := hedgeTestServer(t, 0, http.StatusOK, &calls)
	tool, err := NewWebSearchTool(WebSearchToolOptions{
		Config: WebSearchConfig{Providers: []WebSearchProviderConfig{
			{Name: "broken", Type: "browser", URL: "https://www.google.com/search", TimeoutMS: 1000},
			{Name: "fallback", Type: "tavily", URL: fallback.URL, TimeoutMS: 1000},
		}},
		APIKeys:  map[string]string{"fallback": "k"},
		Renderer: PageRendererFunc(func(context.Context, string) (RenderedPage, error) { panic("test") }),
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		output, err := tool.Run(context.Background(), map[string]any{"query": "diana"})
		if err == nil {
			var result webSearchResult
			err = json.Unmarshal([]byte(output), &result)
			if err == nil && result.Provider != "fallback" {
				err = fmt.Errorf("unexpected provider: %s", result.Provider)
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider panic left search waiting")
	}
}
