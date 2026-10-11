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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var started atomic.Int32
	allStarted := make(chan struct{})
	tool := NewBrowserRenderTool(PageRendererFunc(func(ctx context.Context, url string) (RenderedPage, error) {
		if started.Add(1) == 3 {
			close(allStarted)
		}
		select {
		case <-allStarted:
		case <-ctx.Done():
			return RenderedPage{}, ctx.Err()
		}
		if strings.Contains(url, "broken") {
			return RenderedPage{}, errors.New("timeout")
		}
		return RenderedPage{URL: url, RequestedURL: url, Title: "T " + url, Text: "正文 " + url}, nil
	}))
	output, err := tool.Run(ctx, map[string]any{
		"url":  "https://ai.google.dev/a",
		"urls": []any{"https://deepmind.google/b", "https://ai.google.dev/a", "https://broken.example/c", "https://extra.example/d"},
	})
	if ctx.Err() != nil {
		t.Fatalf("pages did not all start in parallel before timeout: %v", ctx.Err())
	}
	if err != nil {
		t.Fatal(err)
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
