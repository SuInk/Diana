// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type fakeCUACall struct {
	name string
	args map[string]any
}

// fakeCUADriver answers cua-driver tools from canned structured payloads, the
// shapes recorded from cua-driver 0.38 on macOS.
type fakeCUADriver struct {
	mu    sync.Mutex
	calls []fakeCUACall
	image []byte
}

func (f *fakeCUADriver) Close() error { return nil }

func (f *fakeCUADriver) call(_ context.Context, name string, args map[string]any) (cuaDriverResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCUACall{name: name, args: args})
	image := f.image
	f.mu.Unlock()
	structured := func(v any) json.RawMessage { body, _ := json.Marshal(v); return body }
	switch name {
	case "list_apps":
		return cuaDriverResult{Structured: structured(map[string]any{"apps": []any{
			map[string]any{"bundle_id": "com.apple.TextEdit", "name": "TextEdit", "pid": 501, "running": true, "active": true},
			map[string]any{"bundle_id": "com.apple.Notes", "name": "Notes", "pid": 0, "running": false},
		}})}, nil
	case "list_windows":
		return cuaDriverResult{Structured: structured(map[string]any{"windows": []any{
			map[string]any{"app_name": "TextEdit", "title": "未命名", "pid": 501, "window_id": 77, "layer": 0, "z_index": 9, "bounds": map[string]any{"x": 10, "y": 20, "width": 800, "height": 600}},
			map[string]any{"app_name": "TextEdit", "title": "", "pid": 501, "window_id": 78, "layer": 3, "z_index": 10, "bounds": map[string]any{"width": 20, "height": 20}},
			map[string]any{"app_name": "Mystery", "title": "no bundle", "pid": 999, "window_id": 79, "layer": 0, "z_index": 1, "bounds": map[string]any{"width": 100, "height": 100}},
		}})}, nil
	case "get_window_state":
		if args["include_accessibility_tree"] == false {
			return cuaDriverResult{Structured: structured(map[string]any{"app_name": "TextEdit", "window_title": "未命名", "capture_id": "capture_1", "snapshot_id": "s00000001"}), Images: []cuaDriverImage{{Mime: "image/png", Data: image}}}, nil
		}
		return cuaDriverResult{Structured: structured(map[string]any{"snapshot_id": "s0000000a", "truncated": false, "elements": []any{
			map[string]any{"element_index": 0, "role": "AXWindow", "label": "未命名", "actions": []any{"AXRaise"}, "screenshot_frame": map[string]any{"x": 0, "y": 0, "w": 800, "h": 600}},
			map[string]any{"element_index": 3, "role": "AXButton", "label": "保存", "actions": []any{"AXPress"}, "screenshot_frame": map[string]any{"x": 40, "y": 50, "w": 60, "h": 20}},
			map[string]any{"element_index": 4, "role": "AXStaticText", "label": "说明", "value": 12, "screenshot_frame": map[string]any{"x": 1, "y": 2, "w": 3, "h": 4}},
		}})}, nil
	case "check_permissions":
		return cuaDriverResult{Structured: structured(map[string]any{"accessibility": true, "screen_recording": false})}, nil
	}
	return cuaDriverResult{Structured: structured(map[string]any{"effect": "unverifiable"})}, nil
}

func (f *fakeCUADriver) last(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].name == name {
			return f.calls[i].args
		}
	}
	return nil
}

func (f *fakeCUADriver) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.name == name {
			n++
		}
	}
	return n
}

func newFakeCUAAdapter() (*ProcessAdapter, *fakeCUADriver) {
	fake := &fakeCUADriver{image: []byte("\x89PNG fake scene")}
	backend := &cuaDriverBackend{session: fake, snapshots: map[string]string{}, captures: map[string]string{}, longEdge: map[string]int{}}
	return newProcessAdapter(backend, "/fake/cua-driver"), fake
}

// 窗口按 pid 关联 Bundle ID：白名单靠它，查不到归属的窗口和非普通层窗口都不交出去。
func TestCUADriverListsOnlyNamedNormalWindows(t *testing.T) {
	a, _ := newFakeCUAAdapter()
	windows, err := a.ListWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []WindowInfo{{ID: "501:77", AppName: "TextEdit", BundleID: "com.apple.TextEdit", Title: "未命名", Active: true}}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %#v", windows)
	}
}

// 截图长边按窗口点数封顶：截图像素才等于 Command.X/Y 约定的逻辑点。
func TestCUADriverCaptureMatchesLogicalPoints(t *testing.T) {
	a, fake := newFakeCUAAdapter()
	shot, err := a.screenshot(context.Background(), "501:77", "com.apple.TextEdit")
	if err != nil {
		t.Fatal(err)
	}
	args := fake.last("get_window_state")
	if args["max_image_dimension"] != 800 || args["include_accessibility_tree"] != false || args["pid"] != int64(501) || args["window_id"] != int64(77) {
		t.Fatalf("capture args = %#v", args)
	}
	if shot.Mime != "image/png" || shot.Observation == 0 || shot.BundleID != "com.apple.TextEdit" {
		t.Fatalf("shot = %#v", shot)
	}
	fake.image = nil
	if _, err := a.screenshot(context.Background(), "501:77", "com.apple.TextEdit"); ErrorCode(err) != CodePermissionDenied {
		t.Fatalf("missing image should read as a permission problem, got %v", err)
	}
	if _, err := a.screenshot(context.Background(), "not-a-window", ""); ErrorCode(err) != CodeWindowUnknown {
		t.Fatalf("bad window id = %v", err)
	}
}

// 元素点击走完 Diana 自己的观察→比对→确认流程，确认前不下发；下发时用最新快照拼令牌。
func TestCUADriverElementClickNeedsApprovalAndFreshToken(t *testing.T) {
	a, fake := newFakeCUAAdapter()
	ctx := context.Background()
	elements, err := a.Elements(ctx, Command{WindowID: "501:77", ExpectedBundleID: "com.apple.TextEdit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(elements.Elements) != 3 || elements.Elements[1].ID != "3" || !elements.Elements[1].Enabled || elements.Elements[2].Enabled || elements.Elements[2].Value != "12" {
		t.Fatalf("elements = %#v", elements.Elements)
	}
	c := Command{WindowID: "501:77", ExpectedBundleID: "com.apple.TextEdit", ElementID: "3", Observation: elements.Observation}
	if _, err := a.Click(ctx, c); ErrorCode(err) != CodeConfirmationRequired {
		t.Fatalf("first click should wait for approval, got %v", err)
	}
	if fake.count("click") != 0 {
		t.Fatal("input was dispatched before the owner confirmed")
	}
	pending, _ := a.PendingActions(ctx)
	if len(pending) != 1 || pending[0].TargetLabel != "保存" {
		t.Fatalf("pending = %#v", pending)
	}
	if err := a.ConfirmAction(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	result, err := a.Click(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if args := fake.last("click"); args["element_token"] != "s0000000a:3" || args["x"] != nil {
		t.Fatalf("click args = %#v", args)
	}
	if !result.OK || !result.NeedsVerification || result.Evidence == nil || result.Op != OpWindowClick {
		t.Fatalf("result = %#v", result)
	}
	// 结果画面重新登记了观察版本，旧版本已被消费，不能复用。
	if _, err := a.Click(ctx, c); ErrorCode(err) != CodeStaleObservation {
		t.Fatalf("consumed observation reused: %v", err)
	}
}

// 坐标点击带上看过的那张截图的 capture_id，绑定到模型看过的画面。
func TestCUADriverCoordinateClickBindsCapture(t *testing.T) {
	a, fake := newFakeCUAAdapter()
	ctx := context.Background()
	shot, err := a.screenshot(ctx, "501:77", "com.apple.TextEdit")
	if err != nil {
		t.Fatal(err)
	}
	x, y := 12.5, 30.0
	c := Command{WindowID: "501:77", ExpectedBundleID: "com.apple.TextEdit", X: &x, Y: &y, Button: "right", Observation: shot.Observation}
	_, _ = a.Click(ctx, c)
	if err := a.ConfirmAction(ctx, actionKey(c)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Click(ctx, c); err != nil {
		t.Fatal(err)
	}
	if args := fake.last("click"); args["x"] != 12.5 || args["y"] != 30.0 || args["capture_id"] != "capture_1" || args["button"] != "right" || args["element_token"] != nil {
		t.Fatalf("click args = %#v", args)
	}
}

// 菜单快捷键在后台收不到，带 ⌘/⌃/⌥ 的组合键改走前台；普通按键仍在后台。
func TestCUADriverMenuShortcutsUseForeground(t *testing.T) {
	a, fake := newFakeCUAAdapter()
	ctx := context.Background()
	for key, foreground := range map[string]bool{"cmd+s": true, "shift+tab": false, "return": false, "ctrl+a": true} {
		shot, err := a.screenshot(ctx, "501:77", "com.apple.TextEdit")
		if err != nil {
			t.Fatal(err)
		}
		c := Command{WindowID: "501:77", ExpectedBundleID: "com.apple.TextEdit", Key: key, Observation: shot.Observation}
		_, _ = a.PressKey(ctx, c)
		if err := a.ConfirmAction(ctx, actionKey(c)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.PressKey(ctx, c); err != nil {
			t.Fatal(err)
		}
		if got := fake.last("press_key")["delivery_mode"] == "foreground"; got != foreground {
			t.Fatalf("%s foreground = %v, want %v", key, got, foreground)
		}
	}
}

func TestCUADriverKeyAndScrollTranslation(t *testing.T) {
	key, modifiers := splitCUAKey("Command+Shift+S")
	if key != "s" || !reflect.DeepEqual(modifiers, []string{"cmd", "shift"}) {
		t.Fatalf("key=%q modifiers=%v", key, modifiers)
	}
	if key, modifiers = splitCUAKey("Enter"); key != "return" || modifiers != nil {
		t.Fatalf("key=%q modifiers=%v", key, modifiers)
	}
	cases := []struct {
		dx, dy    int
		direction string
		amount    int
	}{
		{0, 120, "down", 3}, {0, -10, "up", 1}, {-200, 30, "left", 5}, {0, 99999, "down", 50}, {0, 0, "", 1},
	}
	for _, tc := range cases {
		direction, amount := cuaScrollDirection(tc.dx, tc.dy)
		if direction != tc.direction || amount != tc.amount {
			t.Fatalf("scroll(%d,%d) = %s %d", tc.dx, tc.dy, direction, amount)
		}
	}
}

func TestCUADriverPermissionsAndToolErrors(t *testing.T) {
	a, _ := newFakeCUAAdapter()
	p, err := a.Permissions(context.Background())
	if err != nil || !p.Accessibility || p.ScreenRecording {
		t.Fatalf("permissions = %#v err=%v", p, err)
	}
	for text, code := range map[string]string{
		"Accessibility permission not granted":                  CodePermissionDenied,
		"element_token s00000001:3 is stale; current s00000002": CodeStaleObservation,
		"window_id_not_found":                                   CodeWindowUnknown,
		"invalid_params: x must be a number":                    CodeBadRequest,
		"something else went wrong":                             CodeHelper,
	} {
		if got := ErrorCode(cuaDriverToolError("click", cuaDriverResult{Text: text})); got != code {
			t.Fatalf("%q -> %s, want %s", text, got, code)
		}
	}
	long := cuaDriverToolError("click", cuaDriverResult{Text: strings.Repeat("x", 1000)})
	if len(long.Error()) > 420 {
		t.Fatalf("tool error is not bounded: %d", len(long.Error()))
	}
}
