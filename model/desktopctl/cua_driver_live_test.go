// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveCUADriverReadOnly 对真实 cua-driver 只做只读调用，打印结构化输出供核对。
// DIANA_CUA_DRIVER_LIVE 指向 cua-driver 可执行文件时才跑；不点击、不输入。
func TestLiveCUADriverReadOnly(t *testing.T) {
	path := os.Getenv("DIANA_CUA_DRIVER_LIVE")
	if path == "" {
		t.Skip("set DIANA_CUA_DRIVER_LIVE to a cua-driver binary")
	}
	session, err := newCUADriverSession(path, "mcp", "--direct")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"check_permissions", map[string]any{"prompt": false}},
		{"list_apps", map[string]any{}},
		{"list_windows", map[string]any{"on_screen_only": true}},
	} {
		if os.Getenv("DIANA_CUA_DRIVER_LIVE_SKIP_LISTS") != "" && call.name != "check_permissions" {
			continue
		}
		result, err := session.call(ctx, call.name, call.args)
		structured := string(result.Structured)
		if len(structured) > 3000 {
			structured = structured[:3000] + "…"
		}
		text := result.Text
		if len(text) > 1500 {
			text = text[:1500] + "…"
		}
		t.Logf("== %s err=%v images=%d\nstructured=%s\ntext=%s", call.name, err, len(result.Images), structured, text)
	}
	pid, window := os.Getenv("DIANA_CUA_DRIVER_LIVE_PID"), os.Getenv("DIANA_CUA_DRIVER_LIVE_WINDOW")
	if pid == "" || window == "" {
		return
	}
	for _, format := range []string{"elements"} {
		result, err := session.call(ctx, "get_window_state", map[string]any{"pid": mustAtoi(t, pid), "window_id": mustAtoi(t, window), "tree_format": format, "max_elements": 12})
		structured := string(result.Structured)
		if len(structured) > 4000 {
			structured = structured[:4000] + "…"
		}
		for _, image := range result.Images {
			t.Logf("image mime=%s bytes=%d", image.Mime, len(image.Data))
		}
		t.Logf("== get_window_state(%s) err=%v\nstructured=%s", format, err, structured)
		shot, err := session.call(ctx, "get_window_state", map[string]any{"pid": mustAtoi(t, pid), "window_id": mustAtoi(t, window), "include_accessibility_tree": false})
		t.Logf("== screenshot-only err=%v images=%d structured=%.1500s", err, len(shot.Images), string(shot.Structured))
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestLiveCUADriverAdapterObservation 走完整的 ProcessAdapter：列窗口、两次截图比对、读元素。
// 只读；DIANA_CUA_DRIVER_LIVE_BUNDLE 指定要观察的应用（如 com.apple.iCal）。
func TestLiveCUADriverAdapterObservation(t *testing.T) {
	path, bundle := os.Getenv("DIANA_CUA_DRIVER_LIVE"), os.Getenv("DIANA_CUA_DRIVER_LIVE_BUNDLE")
	if path == "" || bundle == "" {
		t.Skip("set DIANA_CUA_DRIVER_LIVE and DIANA_CUA_DRIVER_LIVE_BUNDLE")
	}
	session, err := newCUADriverSession(path, "mcp", "--direct")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cuaDriverBackend{session: session, snapshots: map[string]string{}, captures: map[string]string{}, longEdge: map[string]int{}}
	a := newProcessAdapter(backend, path)
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	windows, err := a.ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var target WindowInfo
	for _, window := range windows {
		if window.BundleID == bundle {
			target = window
			break
		}
	}
	if target.ID == "" {
		t.Fatalf("no window for %s among %d windows", bundle, len(windows))
	}
	first, err := a.screenshot(ctx, target.ID, bundle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.screenshot(ctx, target.ID, bundle)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("window=%+v shot_bytes=%d identical_captures=%v", target, len(first.Data), first.Data == second.Data)
	elements, err := a.Elements(ctx, Command{WindowID: target.ID, ExpectedBundleID: bundle})
	if err != nil {
		t.Fatalf("elements: %v", err)
	}
	t.Logf("elements=%d truncated=%v observation=%d", len(elements.Elements), elements.Truncated, elements.Observation)
	for i, el := range elements.Elements {
		if i >= 5 {
			break
		}
		t.Logf("  [%s] %s %q (%.0f,%.0f %.0fx%.0f) enabled=%v", el.ID, el.Role, el.Label, el.X, el.Y, el.Width, el.Height, el.Enabled)
	}
}

// TestLiveCUADriverInstallPinned 真的从 GitHub 下载锁定版本，核对两个 SHA-256，再用装好的
// 驱动列一次窗口。DIANA_CUA_DRIVER_LIVE_INSTALL=1 时才跑（约 53MB）。
func TestLiveCUADriverInstallPinned(t *testing.T) {
	if os.Getenv("DIANA_CUA_DRIVER_LIVE_INSTALL") == "" {
		t.Skip("set DIANA_CUA_DRIVER_LIVE_INSTALL=1")
	}
	// DIANA_CUA_DRIVER_LIVE_ARCHIVE 指向已下载的官方压缩包时，从本地服务读它，
	// 照样核对锁定的两个哈希；GitHub 不通时也能验证安装流程。
	var mirror func(context.Context, string) string
	if archive := os.Getenv("DIANA_CUA_DRIVER_LIVE_ARCHIVE"); archive != "" {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, archive) }))
		defer server.Close()
		mirror = func(context.Context, string) string { return server.URL + "/archive.tar.gz" }
	}
	installer := NewCUADriverInstaller(t.TempDir(), nil, mirror)
	if installer == nil {
		t.Skip("no pinned cua-driver for this platform")
	}
	if err := installer.install(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := installer.Installed()
	if path == "" {
		t.Fatal("installed binary failed verification")
	}
	a, err := NewCUADriverAdapter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	windows, err := a.ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := a.Permissions(ctx)
	t.Logf("installed=%s windows=%d permissions=%+v err=%v", path, len(windows), permissions, err)
}

// TestLiveCUADriverWriteAndSave 是 #948 的实机写入验收：用「文本编辑」打开临时目录里的
// 空白文本文件，经 Diana 完整的观察→待确认→确认→重试链路输入一行字并按 ⌘S 保存，再读文件
// 核对。会在桌面上开一个窗口并发送键盘输入，只在 DIANA_CUA_DRIVER_LIVE_WRITE=1 时运行。
func TestLiveCUADriverWriteAndSave(t *testing.T) {
	path := os.Getenv("DIANA_CUA_DRIVER_LIVE")
	if path == "" || os.Getenv("DIANA_CUA_DRIVER_LIVE_WRITE") != "1" {
		t.Skip("set DIANA_CUA_DRIVER_LIVE and DIANA_CUA_DRIVER_LIVE_WRITE=1")
	}
	const bundle = "com.apple.TextEdit"
	// TextEdit reports the resolved path (/private/var/...); match on that, and
	// give the file a unique name so a window left by an earlier run is never
	// mistaken for this one.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("diana-e2e-%d.txt", time.Now().UnixNano())
	doc := filepath.Join(dir, name)
	if err := os.WriteFile(doc, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// -g keeps TextEdit in the background: the driver types into it without
	// focus, so keystrokes of whoever is using this Mac never land in the file.
	if out, err := exec.Command("open", "-g", "-a", "TextEdit", doc).CombinedOutput(); err != nil {
		t.Fatalf("open TextEdit: %v %s", err, out)
	}
	t.Cleanup(func() {
		script := `tell application "TextEdit" to close (every document whose path is "` + doc + `") saving no`
		_ = exec.Command("osascript", "-e", script).Run()
	})
	a, err := NewCUADriverAdapter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var window WindowInfo
	for attempt := 0; attempt < 40 && window.ID == ""; attempt++ {
		time.Sleep(250 * time.Millisecond)
		windows, err := a.ListWindows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range windows {
			if w.BundleID == bundle && strings.Contains(w.Title, strings.TrimSuffix(name, ".txt")) {
				window = w
			}
		}
	}
	if window.ID == "" {
		t.Fatal("TextEdit window for the test document did not appear")
	}
	t.Logf("window=%+v", window)

	// confirmAndRun drives one action through Diana's approval gate: the first
	// attempt must stop at confirmation_required without sending input.
	confirmAndRun := func(label string, c Command, run func(Command) (ActionResult, error)) ActionResult {
		t.Helper()
		if _, err := run(c); ErrorCode(err) != CodeConfirmationRequired {
			t.Fatalf("%s: first attempt should wait for approval, got %v", label, err)
		}
		if err := a.ConfirmAction(ctx, actionKey(c)); err != nil {
			t.Fatalf("%s: confirm: %v", label, err)
		}
		result, err := run(c)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if !result.OK || !result.NeedsVerification || result.Evidence == nil {
			t.Fatalf("%s: result = %+v", label, result)
		}
		return result
	}

	var elements ElementsPayload
	var textArea Element
	for attempt := 0; attempt < 10 && textArea.ID == ""; attempt++ {
		elements, err = a.Elements(ctx, Command{WindowID: window.ID, ExpectedBundleID: bundle})
		if err != nil {
			t.Logf("elements attempt %d: %v", attempt, err)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		for _, el := range elements.Elements {
			if el.Role == "AXTextArea" && el.Enabled {
				textArea = el
			}
		}
	}
	if textArea.ID == "" {
		t.Fatalf("no editable text area among %d elements", len(elements.Elements))
	}
	const line = "Diana 桌面控制验收 cua-driver"
	typed := confirmAndRun("type", Command{WindowID: window.ID, ExpectedBundleID: bundle, ElementID: textArea.ID, Text: line, Observation: elements.Observation}, func(c Command) (ActionResult, error) {
		return a.TypeText(ctx, c)
	})
	// The evidence is captured right after input; the window keeps updating
	// (title gains "Edited"), so look again before the next action, as a model would.
	_ = typed
	time.Sleep(500 * time.Millisecond)
	if out, err := exec.Command("osascript", "-e", `tell application "TextEdit" to get text of (first document whose path is "`+doc+`")`).CombinedOutput(); err == nil {
		t.Logf("document text before save: %q", strings.TrimSpace(string(out)))
	}
	fresh, err := a.screenshot(ctx, window.ID, bundle)
	if err != nil {
		t.Fatal(err)
	}
	confirmAndRun("save", Command{WindowID: window.ID, ExpectedBundleID: bundle, Key: "cmd+s", Observation: fresh.Observation}, func(c Command) (ActionResult, error) {
		return a.PressKey(ctx, c)
	})
	var saved []byte
	for attempt := 0; attempt < 20; attempt++ {
		time.Sleep(250 * time.Millisecond)
		if saved, _ = os.ReadFile(doc); strings.Contains(string(saved), line) {
			break
		}
	}
	if strings.TrimRight(string(saved), "\n") != line {
		t.Fatalf("saved file = %q, want exactly %q", saved, line)
	}
	t.Logf("saved %d bytes: %q", len(saved), saved)
}
