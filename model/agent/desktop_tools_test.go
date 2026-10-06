// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/desktopctl"
)

type stubDesktopBridge struct {
	ready   bool
	last    desktopctl.Command
	result  desktopctl.Result
	failure error
}

func (b *stubDesktopBridge) Ready() bool { return b.ready }

func (b *stubDesktopBridge) Jobs() *desktopctl.JobManager { return nil }

func (b *stubDesktopBridge) Dispatch(_ context.Context, cmd desktopctl.Command) (desktopctl.Result, error) {
	b.last = cmd
	if b.failure != nil {
		return desktopctl.Result{}, b.failure
	}
	return b.result, nil
}

func desktopToolNames(t *testing.T, cfg Config) map[string]bool {
	t.Helper()
	registry, err := NewDefaultToolRegistry(cfg)
	if err != nil {
		t.Fatalf("创建注册表失败：%v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	names := map[string]bool{}
	for _, name := range registry.Names() {
		if strings.HasPrefix(name, "desktop_") {
			names[name] = true
		}
	}
	return names
}

func TestDesktopToolsUnregisteredWithoutBridge(t *testing.T) {
	names := desktopToolNames(t, Config{WorkDir: t.TempDir()})
	if len(names) != 0 {
		t.Fatalf("没有控制面时不该登记桌面工具，得到 %v", names)
	}
}

func TestDesktopToolsRegisteredWithBridge(t *testing.T) {
	names := desktopToolNames(t, Config{WorkDir: t.TempDir(), DesktopControl: &stubDesktopBridge{ready: true}})
	for _, want := range []string{
		"desktop_windows", "desktop_screenshot", "desktop_click", "desktop_type", "desktop_key",
		"desktop_job_create", "desktop_job_status", "desktop_job_pause", "desktop_job_resume",
		"desktop_job_wait_confirm", "desktop_job_confirm", "desktop_job_cancel",
	} {
		if !names[want] {
			t.Errorf("缺少工具 %s，实际 %v", want, names)
		}
	}
}

func TestDesktopScreenshotAttachesImageParts(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	data, _ := json.Marshal(desktopctl.ScreenshotPayload{
		WindowID: "w1", AppName: "Safari", Mime: "image/png", Data: png,
	})
	bridge := &stubDesktopBridge{ready: true, result: desktopctl.Result{OK: true, Data: data}}
	tool := &DesktopScreenshotTool{base: desktopControlToolBase{root: t.TempDir(), bridge: bridge}}
	out, err := tool.Run(context.Background(), map[string]any{"window_id": "w1"})
	if err != nil {
		t.Fatalf("截图失败：%v", err)
	}
	if !strings.Contains(out, "w1") {
		t.Fatalf("输出应含 window_id，得到 %s", out)
	}
	parts := tool.ToolResultParts(out)
	if len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("应附带图片 ContentPart，得到 %+v", parts)
	}
}

func TestDesktopToolsExcludedFromExtensionScope(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), DesktopControl: &stubDesktopBridge{ready: true}}
	if cfg.ExtensionScope().DesktopControl != nil {
		t.Fatal("ExtensionScope 不该带上桌面控制句柄")
	}
}
