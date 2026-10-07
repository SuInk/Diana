// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

type PageScreenshot struct {
	URL    string
	Title  string
	Stable bool
	PNG    []byte
}

type PageScreenshotter interface {
	Screenshot(context.Context, string) (PageScreenshot, error)
}

// Screenshot uses a fresh profile and renders the actual webpage, including
// GitHub URLs for which textual rendering may otherwise use the official API.
func (b *SandboxedHeadlessBrowser) Screenshot(ctx context.Context, rawURL string) (PageScreenshot, error) {
	if b == nil {
		return PageScreenshot{}, errors.New("headless browser is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()
	if err := validateSandboxedBrowserURL(ctx, rawURL); err != nil {
		return PageScreenshot{}, err
	}
	var data []byte
	page, err := b.renderBrowser(ctx, ctx, rawURL, &data)
	if err != nil {
		return PageScreenshot{}, err
	}
	return PageScreenshot{URL: page.URL, Title: page.Title, Stable: page.Stable, PNG: data}, nil
}

type WebpageScreenshotTool struct {
	browser PageScreenshotter
	mu      sync.Mutex
	parts   []llm.ContentPart
}

func NewWebpageScreenshotTool(browser PageScreenshotter) *WebpageScreenshotTool {
	if browser == nil {
		browser = NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{})
	}
	return &WebpageScreenshotTool{browser: browser}
}

func (t *WebpageScreenshotTool) Name() string { return "webpage_screenshot" }
func (t *WebpageScreenshotTool) Description() string {
	return "截取公网页面的可见窗口并附加真实画面。每次使用独立临时浏览器，不带主人或用户的登录态；拒绝本机、内网和带账号密码的 URL。需要登录浏览器画面时用已授权的 browser_screenshot。"
}
func (t *WebpageScreenshotTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"url"}, map[string]any{"url": toolStringParam("公网 HTTP(S) 网页完整 URL")})
}
func (t *WebpageScreenshotTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	page, err := t.browser.Screenshot(ctx, stringFromInput(input, "url"))
	if err != nil {
		return "", err
	}
	part, err := screenshotImagePart(page.PNG)
	if err != nil {
		return "", err
	}
	output, err := json.Marshal(map[string]any{"url": page.URL, "title": page.Title, "stable": page.Stable, "sandboxed": true, "bytes": len(page.PNG)})
	if err != nil {
		return "", err
	}
	t.parts = []llm.ContentPart{part}
	return string(output), nil
}
func (t *WebpageScreenshotTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
