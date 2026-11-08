// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build browser_integration

package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 常驻搜索浏览器要复用进程，但每次渲染之间不能共享 Cookie。
func TestWarmSearchBrowserReusesProcessAndIsolatesCookies(t *testing.T) {
	t.Cleanup(func() {
		warmSearchBrowser.mu.Lock()
		defer warmSearchBrowser.mu.Unlock()
		if warmSearchBrowser.timer != nil {
			warmSearchBrowser.timer.Stop()
		}
		if warmSearchBrowser.session != nil {
			warmSearchBrowser.session.close()
			warmSearchBrowser.session = nil
		}
	})
	renderer := NewSandboxedBrowser(SandboxedBrowserConfig{Timeout: 30 * time.Second, ReuseProcess: true})
	started := time.Now()
	if _, err := renderer.Render(context.Background(), "https://httpbin.org/cookies/set?diana_isolation=leaked"); err != nil {
		t.Fatal(err)
	}
	first := time.Since(started)
	warmSearchBrowser.mu.Lock()
	session := warmSearchBrowser.session
	warmSearchBrowser.mu.Unlock()
	if session == nil {
		t.Fatal("warm browser was not kept")
	}
	started = time.Now()
	page, err := renderer.Render(context.Background(), "https://httpbin.org/cookies")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first=%s second=%s text=%q", first, time.Since(started), truncateText(page.Text, 200))
	warmSearchBrowser.mu.Lock()
	reused := warmSearchBrowser.session == session
	warmSearchBrowser.mu.Unlock()
	if !reused {
		t.Fatal("second render started a new browser")
	}
	if strings.Contains(page.Text, "leaked") {
		t.Fatalf("cookie leaked between renders: %q", page.Text)
	}
}
