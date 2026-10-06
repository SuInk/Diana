// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/SuInk/diana/model/desktopctl"
	"github.com/SuInk/diana/model/llm"
)

// DesktopControlBridge 是桌面控制的下发入口，由 model/desktopctl.Hub 实现。
//
// 与 BrowserControlBridge / 内置浏览器都是两回事：这边操作的是操作系统窗口，
// 不碰浏览器扩展，也不走 CDP。阶段 1 只有列窗口与截图。
type DesktopControlBridge interface {
	Ready() bool
	Dispatch(ctx context.Context, cmd desktopctl.Command) (desktopctl.Result, error)
}

type desktopControlToolBase struct {
	root   string
	bridge DesktopControlBridge
}

func (b desktopControlToolBase) dispatch(ctx context.Context, cmd desktopctl.Command) (desktopctl.Result, error) {
	if b.bridge == nil {
		return desktopctl.Result{}, errors.New("桌面控制未启用：需要打开桌面控制总开关、连接执行器，并给本机器人授权")
	}
	return b.bridge.Dispatch(ctx, cmd)
}

func desktopJSONOutput(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	var pretty json.RawMessage = data
	var buf any
	if err := json.Unmarshal(data, &buf); err == nil {
		body, err := json.MarshalIndent(buf, "", "  ")
		if err == nil {
			pretty = body
		}
	}
	return string(pretty), nil
}

// DesktopWindowsTool 列出已授权应用的窗口。
type DesktopWindowsTool struct {
	base desktopControlToolBase
}

func (t *DesktopWindowsTool) Name() string { return "desktop_windows" }

func (t *DesktopWindowsTool) Description() string {
	return `列出本机已授权应用的窗口（应用显示名 + 窗口标题）。拿 window_id 给 desktop_screenshot 用。未授权应用的窗口不可见。`
}

func (t *DesktopWindowsTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"connection": toolStringParam("连着多个桌面执行器时点名"),
	})
}

func (t *DesktopWindowsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	result, err := t.base.dispatch(ctx, desktopctl.Command{
		Op:         desktopctl.OpWindowsList,
		Connection: stringFromInput(input, "connection"),
	})
	if err != nil {
		return "", err
	}
	return desktopJSONOutput(result.Data)
}

// DesktopScreenshotTool 截取已授权窗口。
type DesktopScreenshotTool struct {
	base desktopControlToolBase

	mu    sync.Mutex
	parts []llm.ContentPart
}

func (t *DesktopScreenshotTool) Name() string { return "desktop_screenshot" }

func (t *DesktopScreenshotTool) Description() string {
	return `截取本机已授权应用窗口的画面，图片直接给你看。先用 desktop_windows 拿到 window_id。需要本机 Screen Recording 等系统权限；权限不足时会返回可解释错误。`
}

func (t *DesktopScreenshotTool) RepeatableCalls() bool { return true }

func (t *DesktopScreenshotTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}

func (t *DesktopScreenshotTool) setParts(parts []llm.ContentPart) {
	t.mu.Lock()
	t.parts = parts
	t.mu.Unlock()
}

func (t *DesktopScreenshotTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"window_id":  toolStringParam("窗口 ID，来自 desktop_windows；省略则用当前活动的已授权窗口"),
		"connection": toolStringParam("连着多个桌面执行器时点名"),
		"path":       toolStringParam("相对工作目录的保存路径，可选"),
	})
}

func (t *DesktopScreenshotTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.setParts(nil)
	result, err := t.base.dispatch(ctx, desktopctl.Command{
		Op:         desktopctl.OpWindowScreenshot,
		Connection: stringFromInput(input, "connection"),
		WindowID:   stringFromInput(input, "window_id"),
	})
	if err != nil {
		return "", err
	}
	var payload desktopctl.ScreenshotPayload
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &payload); err != nil {
			return "", fmt.Errorf("截图回执无法解析：%w", err)
		}
	}
	if payload.Data == "" {
		return "", errors.New("执行器没有返回截图数据")
	}
	mime := payload.Mime
	if mime == "" {
		mime = "image/png"
	}
	t.setParts([]llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "data:" + mime + ";base64," + payload.Data}})

	out := map[string]any{
		"window_id": payload.WindowID,
		"app_name":  payload.AppName,
		"bundle_id": payload.BundleID,
		"title":     payload.Title,
		"note":      "截图已附在这条结果里",
	}
	if outPath := stringFromInput(input, "path"); outPath != "" && t.base.root != "" {
		path, err := safePath(t.base.root, outPath)
		if err != nil {
			return "", err
		}
		raw, err := base64.StdEncoding.DecodeString(payload.Data)
		if err != nil {
			return "", fmt.Errorf("截图 base64 无效：%w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return "", err
		}
		out["path"] = relPathForOutput(t.base.root, path)
		out["bytes"] = len(raw)
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// RegisterDesktopTools 登记桌面控制只读工具。
//
// 桥为 nil 时一个都不登记：模型看不到工具。
func (r *ToolRegistry) RegisterDesktopTools(root string, cfg Config) {
	if cfg.DesktopControl == nil {
		return
	}
	base := desktopControlToolBase{root: root, bridge: cfg.DesktopControl}
	r.Register(&DesktopWindowsTool{base: base})
	r.Register(&DesktopScreenshotTool{base: base})
}
