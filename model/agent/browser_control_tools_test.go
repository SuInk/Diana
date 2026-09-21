// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/browserctl"
)

// stubBridge 记下下发过的指令，并按预置结果回执。
type stubBridge struct {
	ready   bool
	last    browserctl.Command
	result  browserctl.Result
	failure error
}

func (b *stubBridge) Ready() bool { return b.ready }

func (b *stubBridge) Dispatch(_ context.Context, cmd browserctl.Command) (browserctl.Result, error) {
	b.last = cmd
	if b.failure != nil {
		return browserctl.Result{}, b.failure
	}
	return b.result, nil
}

func browserExtToolNames(t *testing.T, cfg Config) map[string]bool {
	t.Helper()
	registry, err := NewDefaultToolRegistry(cfg)
	if err != nil {
		t.Fatalf("创建注册表失败：%v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	names := map[string]bool{}
	for _, name := range registry.Names() {
		if strings.HasPrefix(name, "browser_ext_") {
			names[name] = true
		}
	}
	return names
}

func TestBrowserControlToolsUnregisteredWithoutBridge(t *testing.T) {
	names := browserExtToolNames(t, Config{WorkDir: t.TempDir()})
	if len(names) != 0 {
		t.Fatalf("没有控制面时不该登记扩展工具，得到 %v", names)
	}
}

func TestBrowserControlToolsRegisteredWithBridge(t *testing.T) {
	names := browserExtToolNames(t, Config{WorkDir: t.TempDir(), BrowserControl: &stubBridge{ready: true}})
	for _, want := range []string{
		"browser_ext_tabs",
		"browser_ext_read",
		"browser_ext_screenshot",
		"browser_ext_open",
		"browser_ext_click",
		"browser_ext_type",
	} {
		if !names[want] {
			t.Errorf("缺少工具 %s，实际 %v", want, names)
		}
	}
}

func TestBrowserControlToolsExcludedFromExtensionScope(t *testing.T) {
	// 共享扩展底座按 ExtensionScope 取字段并算缓存键。控制面句柄不可序列化，
	// 也不该按机器人拆出第二套 MCP 进程，所以必须不在其中。
	cfg := Config{WorkDir: t.TempDir(), BrowserControl: &stubBridge{ready: true}}
	if cfg.ExtensionScope().BrowserControl != nil {
		t.Fatal("ExtensionScope 不该带上控制面句柄")
	}
	if _, err := json.Marshal(cfg.ExtensionScope()); err != nil {
		t.Fatalf("扩展作用域配置必须可序列化：%v", err)
	}
}

func TestBrowserExtReadPassesParamsAndClampsMaxChars(t *testing.T) {
	bridge := &stubBridge{ready: true, result: browserctl.Result{OK: true, Data: json.RawMessage(`{"text":"正文"}`)}}
	tool := &BrowserExtReadTool{base: browserControlToolBase{root: t.TempDir(), bridge: bridge}, maxChars: 100}
	out, err := tool.Run(context.Background(), map[string]any{
		"tab_id":    float64(7),
		"selector":  "#main",
		"max_chars": float64(5000),
	})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if !strings.Contains(out, "正文") {
		t.Fatalf("应把回执原样交给模型，得到 %s", out)
	}
	if bridge.last.Op != browserctl.OpPageRead || bridge.last.TabID != 7 || bridge.last.Selector != "#main" {
		t.Fatalf("指令参数没传对：%+v", bridge.last)
	}
	if bridge.last.MaxChars != 100 {
		t.Fatalf("超过工具上限的 max_chars 应被夹回去，得到 %d", bridge.last.MaxChars)
	}
}

func TestBrowserExtToolsSurfaceBridgeError(t *testing.T) {
	bridge := &stubBridge{failure: errors.New("浏览器控制当前只读，点击、输入和导航都没有授权")}
	tool := &BrowserExtClickTool{base: browserControlToolBase{root: t.TempDir(), bridge: bridge}}
	if _, err := tool.Run(context.Background(), map[string]any{"selector": "#go"}); err == nil ||
		!strings.Contains(err.Error(), "只读") {
		t.Fatalf("控制面拒绝的原因应原样交给模型，得到 %v", err)
	}
}

func TestBrowserExtToolsWithoutBridgeExplainThemselves(t *testing.T) {
	tool := &BrowserExtTabsTool{base: browserControlToolBase{root: t.TempDir()}}
	_, err := tool.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("没有控制面时应说清是没启用，得到 %v", err)
	}
}

func TestBrowserExtScreenshotWritesDataURL(t *testing.T) {
	root := t.TempDir()
	// 1x1 透明 PNG，够验证解码与落盘。
	png, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("准备测试图片失败：%v", err)
	}
	payload := map[string]any{
		"url":   "https://example.com/page",
		"title": "页面",
		"image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	bridge := &stubBridge{ready: true, result: browserctl.Result{OK: true, Data: body}}
	tool := &BrowserExtScreenshotTool{base: browserControlToolBase{root: root, bridge: bridge}}
	out, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("截图失败：%v", err)
	}
	var result struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("解析输出失败：%v", err)
	}
	if result.Bytes != len(png) {
		t.Fatalf("落盘字节数不对：%d != %d", result.Bytes, len(png))
	}
	saved, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(result.Path)))
	if err != nil {
		t.Fatalf("读回截图失败：%v", err)
	}
	if string(saved) != string(png) {
		t.Fatal("落盘内容与回执不一致")
	}
}

func TestBrowserExtScreenshotRejectsGarbage(t *testing.T) {
	bridge := &stubBridge{ready: true, result: browserctl.Result{OK: true, Data: json.RawMessage(`{"image":"不是图片"}`)}}
	tool := &BrowserExtScreenshotTool{base: browserControlToolBase{root: t.TempDir(), bridge: bridge}}
	if _, err := tool.Run(context.Background(), nil); err == nil {
		t.Fatal("非法 base64 应报错，而不是写出一个坏文件")
	}
}

func TestBrowserExtScreenshotStaysInWorkspace(t *testing.T) {
	bridge := &stubBridge{ready: true, result: browserctl.Result{OK: true}}
	tool := &BrowserExtScreenshotTool{base: browserControlToolBase{root: t.TempDir(), bridge: bridge}}
	if _, err := tool.Run(context.Background(), map[string]any{"path": "../escape.png"}); err == nil {
		t.Fatal("路径逃逸应被拦下")
	}
}
