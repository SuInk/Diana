// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func TestPublicScreenshotRejectsUnsafeURLsBeforeLaunchingBrowser(t *testing.T) {
	browser := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{Executable: "missing-test-browser"})
	for _, rawURL := range []string{"http://127.0.0.1/", "http://192.168.1.1/", "http://[::1]/", "file:///etc/passwd", "https://user:secret@1.1.1.1/"} {
		if _, err := browser.Screenshot(context.Background(), rawURL); err == nil || strings.Contains(err.Error(), "executable") {
			t.Fatalf("unsafe URL reached the browser: %s %v", rawURL, err)
		}
	}
}

func TestPublicScreenshotResourceValidation(t *testing.T) {
	ctx := context.Background()
	for _, rawURL := range []string{"http://127.0.0.1/private.png", "http://192.168.1.1/script.js", "file:///etc/passwd", "ws://127.0.0.1/"} {
		if err := validateScreenshotResource(ctx, rawURL, network.ResourceTypeImage); err == nil {
			t.Fatalf("private resource accepted: %s", rawURL)
		}
	}
	for _, rawURL := range []string{"data:image/png;base64,aA==", "blob:https://example.com/1", "https://1.1.1.1/img.png"} {
		if err := validateScreenshotResource(ctx, rawURL, network.ResourceTypeImage); err != nil {
			t.Fatalf("safe resource rejected: %s %v", rawURL, err)
		}
	}
	if err := validateScreenshotResource(ctx, "data:text/html,secret", network.ResourceTypeDocument); err == nil {
		t.Fatal("non-public document accepted")
	}
}

func TestPublicScreenshotIntegration(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	browser := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{Timeout: 30 * time.Second, VirtualTimeBudget: 3 * time.Second})
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
	if os.Getenv("DIANA_HEADLESS_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executable, err := findHeadlessBrowserExecutable("")
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
