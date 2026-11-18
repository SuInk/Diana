// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

type sandboxBrowserStub struct{ personalBrowserStub }

func (*sandboxBrowserStub) PublicSandbox() bool { return true }

func TestSandboxBrowserPagePolicy(t *testing.T) {
	sandbox := browserToolBase{personal: &sandboxBrowserStub{}, sandbox: true}
	personal := browserToolBase{personal: &personalBrowserStub{}, allowedHosts: []string{"example.com"}}
	for _, raw := range []string{"https://www.bilibili.com/toy/HENTI/index.html", "http://example.org:8080/a"} {
		if !sandbox.pageAllowed(raw) {
			t.Fatalf("sandbox rejected %s", raw)
		}
		if personal.pageAllowed(raw) {
			t.Fatalf("personal allowlist opened %s", raw)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "https://user@example.com", "chrome://settings", "javascript:alert(1)"} {
		if sandbox.pageAllowed(raw) {
			t.Fatalf("sandbox allowed %s", raw)
		}
	}
	tree := screenshotFrame{}
	tree.Frame.URL = "https://www.bilibili.com/toy/HENTI/index.html"
	child := screenshotFrame{}
	child.Frame.URL = "https://www.bilibilitoy.com/toy/HENTI/1/index.html"
	blank := screenshotFrame{}
	blank.Frame.URL = "about:blank"
	tree.ChildFrames = []screenshotFrame{child, blank}
	if !sandbox.frameAllowed(tree) {
		t.Fatal("sandbox rejected a cross-origin iframe page")
	}
	evil := screenshotFrame{}
	evil.Frame.URL = "file:///etc/passwd"
	tree.ChildFrames = append(tree.ChildFrames, evil)
	if sandbox.frameAllowed(tree) {
		t.Fatal("sandbox accepted a file frame")
	}
	if strings.Contains(sandbox.personalExpression("1"), "allowed") || !strings.Contains(personal.personalExpression("1"), "example.com") {
		t.Fatal("page guard used the wrong policy")
	}
}

func TestSandboxBrowserRegistersIndexedTools(t *testing.T) {
	sandbox := NewToolRegistry()
	sandbox.RegisterBrowserTools(t.TempDir(), Config{PersonalBrowser: &sandboxBrowserStub{}, BrowserScreenshotRestricted: true})
	for _, name := range []string{"browser_open", "browser_text", "browser_click", "browser_type", "browser_tabs", "browser_scroll", "browser_press_key", "browser_navigate", "browser_wait", "browser_screenshot"} {
		if _, ok := sandbox.getRegistered(name); !ok {
			t.Fatalf("sandbox missing %s", name)
		}
	}
	for _, name := range []string{"browser_eval", "browser_select"} {
		if _, ok := sandbox.getRegistered(name); ok {
			t.Fatalf("sandbox exposes %s", name)
		}
	}
	click, _ := sandbox.getRegistered("browser_click")
	if _, ok := click.(ToolInputSchema).InputSchema()["properties"].(map[string]any)["index"]; !ok {
		t.Fatal("sandbox click has no index")
	}
	family, _ := sandbox.Get(BrowserToolFamilyName)
	if !strings.Contains(family.Description(), "沙箱") || strings.Contains(family.Description(), "带登录态的交互式") {
		t.Fatalf("sandbox family description: %s", family.Description())
	}

	personal := NewToolRegistry()
	personal.RegisterBrowserTools(t.TempDir(), Config{PersonalBrowser: &personalBrowserStub{}, BrowserOperationHosts: []string{"example.com"}})
	for _, name := range []string{"browser_scroll", "browser_press_key", "browser_eval"} {
		if _, ok := personal.getRegistered(name); ok {
			t.Fatalf("personal browser gained %s", name)
		}
	}
	click, _ = personal.getRegistered("browser_click")
	if _, ok := click.(ToolInputSchema).InputSchema()["properties"].(map[string]any)["index"]; ok {
		t.Fatal("personal click gained index")
	}
}

func TestSandboxElementLinesAndStaleIndexes(t *testing.T) {
	state := browserElementState{Above: 120, Below: 900, Elements: []browserElement{
		{Index: 0, Tag: "button", Text: "开始测试"},
		{Index: 1, Tag: "input", Attrs: `type="text" placeholder="昵称"`},
	}}
	got := state.lines()
	for _, want := range []string{"上方还有约 120 像素", "[0]<button>开始测试</button>", `[1]<input type="text" placeholder="昵称"></input>`, "下方还有约 900 像素"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lines missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains((browserElementState{}).lines(), "canvas") {
		t.Fatal("empty viewport gives no hint")
	}
	base := browserToolBase{session: &browserSession{}, sandbox: true}
	base.session.setElements("tab-a", state)
	client := &cdpClient{targetID: "tab-b"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := base.elementByIndex(ctx, client, 0); err == nil || !strings.Contains(err.Error(), "最新") {
		t.Fatalf("index from another tab accepted: %v", err)
	}
	client.targetID = "tab-a"
	if _, err := base.elementByIndex(ctx, client, 7); err == nil {
		t.Fatal("out of range index accepted")
	}
}
