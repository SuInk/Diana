// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build browser_integration

package agent

import (
	"context"
	"flag"
	"strings"
	"testing"
	"time"
)

// browserTestURL 换一个网址跑 TestSandboxedBrowserIntegration：
// go test -tags browser_integration ./model/agent -run TestSandboxedBrowserIntegration -args -browser-test-url=https://...
var browserTestURL = flag.String("browser-test-url", "https://example.com", "TestSandboxedBrowserIntegration 渲染的网址")

func TestSandboxedBrowserIntegration(t *testing.T) {
	targetURL := strings.TrimSpace(*browserTestURL)
	renderer := NewSandboxedBrowser(SandboxedBrowserConfig{
		Timeout:           30 * time.Second,
		VirtualTimeBudget: 3 * time.Second,
	})
	page, err := renderer.Render(context.Background(), targetURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered title=%q url=%q text=%q", page.Title, page.URL, truncateText(page.Text, 500))
	if !page.Sandboxed || (page.Title == "" && page.Text == "") {
		t.Fatalf("page = %#v", page)
	}
	if targetURL == "https://example.com" && (page.Title != "Example Domain" || !strings.Contains(page.Text, "documentation examples")) {
		t.Fatalf("page = %#v", page)
	}
}

func TestSandboxedBrowserDelayedRedirectIntegration(t *testing.T) {
	renderer := NewSandboxedBrowser(SandboxedBrowserConfig{})
	page, err := renderer.Render(context.Background(), "https://67movies.xyz")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered stable=%v waited=%d url=%q title=%q chain=%#v previous=%#v", page.Stable, page.WaitedMS, page.URL, page.Title, page.NavigationChain, page.PreviousPages)
	if !page.Stable || !strings.Contains(page.URL, "youtube.com/watch") {
		t.Fatalf("final page = %#v", page)
	}
	if len(page.NavigationChain) < 2 || len(page.PreviousPages) == 0 || !strings.Contains(page.PreviousPages[0].Text, "Welcome to 67movies") {
		t.Fatalf("navigation evidence missing: %#v", page)
	}
}
