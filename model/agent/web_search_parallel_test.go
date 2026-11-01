// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWebSearchTenQueriesRunBothLanesWithTenConcurrentJobs(t *testing.T) {
	var mu sync.Mutex
	active, peak, calls := 0, 0, 0
	ready := make(chan struct{})
	var release sync.Once
	renderer := PageRendererFunc(func(ctx context.Context, raw string) (RenderedPage, error) {
		mu.Lock()
		active++
		calls++
		peak = max(peak, active)
		if active == 10 {
			release.Do(func() { close(ready) })
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		// Fewer than ten slots cannot reach this barrier: no timing-based speed assertion.
		select {
		case <-ready:
		case <-ctx.Done():
			return RenderedPage{}, ctx.Err()
		}
		return RenderedPage{URL: raw, Text: "results", Links: []RenderedLink{
			{URL: "https://example.com/result", Text: "Result", Snippet: "Verified source candidate"},
		}}, nil
	})
	tool := searchEngineTestTool(t, renderer, "google", "bing")
	queries := make([]string, 11)
	for i := range queries {
		queries[i] = fmt.Sprintf("project-%d documentation", i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := tool.Run(ctx, map[string]any{"queries": queries})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if peak != 10 || calls != 20 || active != 0 {
		t.Fatalf("peak=%d calls=%d active=%d; want 10 concurrent jobs and 20 total lane calls", peak, calls, active)
	}
	if len(result.Queries) != 10 || result.Budget.ProviderCalls != 20 {
		t.Fatalf("unexpected budget/queries: %+v queries=%d", result.Budget, len(result.Queries))
	}
	for _, query := range result.Queries {
		if query.Status == "not_executed" || query.Reason == "provider_call_budget_exhausted" {
			t.Fatalf("default budget skipped query: %+v", query)
		}
	}
}

func TestWebSearchDescribesIndependentBatchDiscovery(t *testing.T) {
	description := (&WebSearchTool{}).Description()
	for _, want := range []string{"优先一次用 queries 批量提交", "最多 10 个", "依赖前一步结果", "urls 批量读原文", "不必凑满"} {
		if !strings.Contains(description, want) {
			t.Fatalf("missing batching guidance %q", want)
		}
	}
}
