// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserRenderOpensSeveralURLsInParallel(t *testing.T) {
	var inFlight, peak atomic.Int32
	tool := NewBrowserRenderTool(PageRendererFunc(func(ctx context.Context, url string) (RenderedPage, error) {
		if now := inFlight.Add(1); now > peak.Load() {
			peak.Store(now)
		}
		defer inFlight.Add(-1)
		time.Sleep(30 * time.Millisecond)
		if strings.Contains(url, "broken") {
			return RenderedPage{}, errors.New("timeout")
		}
		return RenderedPage{URL: url, RequestedURL: url, Title: "T " + url, Text: "正文 " + url}, nil
	}))
	output, err := tool.Run(context.Background(), map[string]any{
		"url":  "https://ai.google.dev/a",
		"urls": []any{"https://deepmind.google/b", "https://ai.google.dev/a", "https://broken.example/c", "https://extra.example/d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() < 2 {
		t.Fatalf("pages were not opened in parallel, peak=%d", peak.Load())
	}
	pages := browserRenderPages(output)
	if len(pages) != 3 {
		t.Fatalf("want 3 pages (deduped, capped), got %d:\n%s", len(pages), output)
	}
	if strings.Contains(output, "extra.example") || !strings.Contains(output, "timeout") {
		t.Fatalf("cap or failure report wrong:\n%s", output)
	}
	steps := []Step{
		{Tool: WebSearchToolName, Input: map[string]any{"query": "x"}, Output: researchProgressSearchOutput},
		{Tool: browserRenderToolName, Output: output},
	}
	if progress := collectResearchProgress(steps); len(progress.read) != 2 {
		t.Fatalf("batch reads should count separately: %#v", progress)
	}
	if guidance := researchObservationGuidance(browserRenderToolName, output, 3); !strings.Contains(guidance, "已读到页面正文") {
		t.Fatalf("batch guidance should use the best page: %s", guidance)
	}
}

func TestBrowserRenderBatchFailsOnlyWhenEveryPageFails(t *testing.T) {
	tool := NewBrowserRenderTool(PageRendererFunc(func(context.Context, string) (RenderedPage, error) {
		return RenderedPage{}, errors.New("blocked")
	}))
	if _, err := tool.Run(context.Background(), map[string]any{"url": "https://a.example", "urls": []string{"https://b.example"}}); err == nil {
		t.Fatal("all pages failed but no error returned")
	}
}
