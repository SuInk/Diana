// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build browser_integration

package agent

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestPublicScreenshotIntegration(t *testing.T) {
	browser := NewSandboxedBrowser(SandboxedBrowserConfig{Timeout: 30 * time.Second, VirtualTimeBudget: 3 * time.Second})
	page, err := browser.Screenshot(context.Background(), "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://example.com/" || page.Title != "Example Domain" || !page.Stable {
		t.Fatalf("unexpected screenshot page: url=%s title=%s stable=%v", page.URL, page.Title, page.Stable)
	}
	if _, err := screenshotImagePart(page.PNG); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %d PNG bytes from %s", len(page.PNG), page.URL)
}

func TestRestrictedCDPScreenshotIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executable, err := findChromeExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := newBrowserSandboxDirs("diana-cdp-screenshot-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer dirs.remove()
	process, err := launchSandboxedChrome(ctx, executable, dirs.root, dirs.profile, dirs.cache, dirs.crash, sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	defer process.stop()
	allocator, cancelAllocator := chromedp.NewRemoteAllocator(ctx, process.wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	if err := chromedp.Run(browser, chromedp.Navigate("https://example.com")); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(process.wsURL)
	if err != nil {
		t.Fatal(err)
	}
	tool := &BrowserScreenshotTool{base: browserToolBase{root: t.TempDir(), cdpURL: "http://" + parsed.Host, timeout: 10 * time.Second}, restricted: true, allowedHosts: []string{"example.com"}}
	output, err := tool.Run(ctx, map[string]any{"url": "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.ToolResultParts(output)) != 1 || strings.Contains(output, "path") {
		t.Fatal("restricted screenshot did not provide an image without file access")
	}
	t.Logf("captured explicit CDP page: %s", output)
}
