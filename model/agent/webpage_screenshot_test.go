// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestPublicScreenshotRejectsUnsafeURLsBeforeLaunchingBrowser(t *testing.T) {
	browser := NewSandboxedBrowser(SandboxedBrowserConfig{Executable: "missing-test-browser"})
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

// 10-15「发下标普500的k线」：搜到行情页后说「没接行情插件发不了」。有 remote_image 时
// 先找直链下载原图，只有截图工具时才按截图处理。
func TestSystemPromptRoutesChartRequests(t *testing.T) {
	prompt := func(tools ...Tool) string {
		registry := NewToolRegistry()
		for _, tool := range tools {
			registry.Register(tool)
		}
		runner, err := NewRunner(&scriptedClient{}, Config{WorkDir: t.TempDir()}, registry)
		if err != nil {
			t.Fatal(err)
		}
		return runner.systemPrompt()
	}
	if strings.Contains(prompt(), "K 线") {
		t.Fatal("chart rule should only appear when an image tool is registered")
	}
	screenshot := NewWebpageScreenshotTool(NewSandboxedBrowser(SandboxedBrowserConfig{}))
	if got := prompt(screenshot); !strings.Contains(got, "就是截图任务") || strings.Contains(got, "remote_image 的 view") {
		t.Fatal("screenshot-only prompt should treat charts as screenshots")
	}
	got := prompt(screenshot, &countingTool{name: "remote_image"})
	for _, want := range []string{"remote_image view 确认画面", "拿不到直链再用 webpage_screenshot"} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
	if strings.Contains(got, "就是截图任务") {
		t.Fatal("with remote_image the prompt should prefer direct download over screenshots")
	}
}
