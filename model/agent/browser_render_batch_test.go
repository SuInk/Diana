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

// 一页慢、另一页早读完：过了软期限就先返回读到的那页，慢的标成还在读；随后单独再读
// 那一页时接上同一次读取，不重开。
func TestBrowserRenderBatchReturnsReadyPagesAfterSoftDeadline(t *testing.T) {
	previous := browserRenderBatchSoftDeadline
	browserRenderBatchSoftDeadline = 30 * time.Millisecond
	defer func() { browserRenderBatchSoftDeadline = previous }()

	release := make(chan struct{})
	var slowRenders atomic.Int32
	tool := NewBrowserRenderTool(PageRendererFunc(func(ctx context.Context, url string) (RenderedPage, error) {
		if strings.Contains(url, "slow") {
			slowRenders.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
				return RenderedPage{}, ctx.Err()
			}
		}
		return RenderedPage{URL: url, RequestedURL: url, Title: "T", Text: "正文 " + url}, nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	output, err := tool.Run(ctx, map[string]any{"url": "https://fast.example/a", "urls": []any{"https://slow.example/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("批量读取不该等慢的那一页：%s", time.Since(started))
	}
	if !strings.Contains(output, "正文 https://fast.example/a") || !strings.Contains(output, `"pending":true`) || !strings.Contains(output, "https://slow.example/b") {
		t.Fatalf("应当返回读到的那页并把慢的标成还在读：\n%s", output)
	}

	// 调用方的 context 结束了，后台那一页照样读完。
	cancel()
	close(release)
	followUp, err := tool.Run(context.Background(), map[string]any{"url": "https://slow.example/b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(followUp, "正文 https://slow.example/b") {
		t.Fatalf("单独再读应当拿到那一页：\n%s", followUp)
	}
	if got := slowRenders.Load(); got != 1 {
		t.Fatalf("慢的那一页应当只读一次，实际 %d 次", got)
	}
}

// 还没有任何一页读到时不提前返回：过了软期限也接着等，第一页读到就返回。
func TestBrowserRenderBatchWaitsForFirstPage(t *testing.T) {
	previous := browserRenderBatchSoftDeadline
	browserRenderBatchSoftDeadline = 10 * time.Millisecond
	defer func() { browserRenderBatchSoftDeadline = previous }()

	tool := NewBrowserRenderTool(PageRendererFunc(func(ctx context.Context, url string) (RenderedPage, error) {
		delay := 80 * time.Millisecond
		if strings.Contains(url, "slower") {
			delay = 3 * time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return RenderedPage{}, ctx.Err()
		}
		return RenderedPage{URL: url, RequestedURL: url, Title: "T", Text: "正文 " + url}, nil
	}))
	started := time.Now()
	output, err := tool.Run(context.Background(), map[string]any{"url": "https://slow.example/a", "urls": []any{"https://slower.example/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("应当等到第一页读到就返回，实际 %s", elapsed)
	}
	if !strings.Contains(output, "正文 https://slow.example/a") || !strings.Contains(output, `"pending":true`) {
		t.Fatalf("output:\n%s", output)
	}
}
