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
// 不碰浏览器扩展，也不走 CDP。阶段 1 只读；阶段 2 在策略打开 write_enabled 后可点击与输入。
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

// DesktopClickTool 在已授权窗口内点击。
type DesktopClickTool struct {
	base desktopControlToolBase
}

func (t *DesktopClickTool) Name() string { return "desktop_click" }

func (t *DesktopClickTool) Description() string {
	return `在本机已授权应用窗口内点击。坐标相对窗口左上角，可先 desktop_screenshot 估位置。需要 WriteEnabled 与 macOS Accessibility；接管中会被拒。不要点付款、删除、系统设置等敏感控件，除非主人明确要求。`
}

func (t *DesktopClickTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"x", "y"}, map[string]any{
		"x":          toolNumberParam("相对窗口左上角的 X（逻辑像素）"),
		"y":          toolNumberParam("相对窗口左上角的 Y（逻辑像素）"),
		"button":     toolStringParam("left（默认）/ right / middle"),
		"window_id":  toolStringParam("窗口 ID，来自 desktop_windows"),
		"connection": toolStringParam("连着多个桌面执行器时点名"),
	})
}

func (t *DesktopClickTool) Run(ctx context.Context, input map[string]any) (string, error) {
	x, okX := numberFromInput(input, "x")
	y, okY := numberFromInput(input, "y")
	if !okX || !okY {
		return "", errors.New("desktop_click 需要 x 与 y")
	}
	result, err := t.base.dispatch(ctx, desktopctl.Command{
		Op:         desktopctl.OpWindowClick,
		Connection: stringFromInput(input, "connection"),
		WindowID:   stringFromInput(input, "window_id"),
		X:          &x,
		Y:          &y,
		Button:     stringFromInput(input, "button"),
	})
	if err != nil {
		return "", err
	}
	return desktopJSONOutput(result.Data)
}

// DesktopTypeTool 向已授权窗口输入文字。
type DesktopTypeTool struct {
	base desktopControlToolBase
}

func (t *DesktopTypeTool) Name() string { return "desktop_type" }

func (t *DesktopTypeTool) Description() string {
	return `在本机已授权应用窗口里输入文字。需要 WriteEnabled 与 Accessibility。不要输入密码、验证码或支付信息。`
}

func (t *DesktopTypeTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"text"}, map[string]any{
		"text":       toolStringParam(""),
		"window_id":  toolStringParam("窗口 ID，来自 desktop_windows"),
		"connection": toolStringParam("连着多个桌面执行器时点名"),
	})
}

func (t *DesktopTypeTool) Run(ctx context.Context, input map[string]any) (string, error) {
	result, err := t.base.dispatch(ctx, desktopctl.Command{
		Op:         desktopctl.OpWindowType,
		Connection: stringFromInput(input, "connection"),
		WindowID:   stringFromInput(input, "window_id"),
		Text:       stringFromInput(input, "text"),
	})
	if err != nil {
		return "", err
	}
	return desktopJSONOutput(result.Data)
}

// DesktopKeyTool 向已授权窗口发送按键。
type DesktopKeyTool struct {
	base desktopControlToolBase
}

func (t *DesktopKeyTool) Name() string { return "desktop_key" }

func (t *DesktopKeyTool) Description() string {
	return `在本机已授权应用窗口里按键或组合键（如 Return、Tab、cmd+c）。需要 WriteEnabled 与 Accessibility。`
}

func (t *DesktopKeyTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"key"}, map[string]any{
		"key":        toolStringParam("按键名，如 Return、Tab、Escape、cmd+c"),
		"window_id":  toolStringParam("窗口 ID，来自 desktop_windows"),
		"connection": toolStringParam("连着多个桌面执行器时点名"),
	})
}

func (t *DesktopKeyTool) Run(ctx context.Context, input map[string]any) (string, error) {
	result, err := t.base.dispatch(ctx, desktopctl.Command{
		Op:         desktopctl.OpWindowKey,
		Connection: stringFromInput(input, "connection"),
		WindowID:   stringFromInput(input, "window_id"),
		Key:        stringFromInput(input, "key"),
	})
	if err != nil {
		return "", err
	}
	return desktopJSONOutput(result.Data)
}

// RegisterDesktopTools 登记桌面控制工具（只读 + 写操作）。
//
// 桥为 nil 时一个都不登记。写操作工具照样登记：能不能用由 desktopctl 的
// WriteEnabled 与接管状态逐条判断。
func (r *ToolRegistry) RegisterDesktopTools(root string, cfg Config) {
	if cfg.DesktopControl == nil {
		return
	}
	base := desktopControlToolBase{root: root, bridge: cfg.DesktopControl}
	r.Register(&DesktopWindowsTool{base: base})
	r.Register(&DesktopScreenshotTool{base: base})
	r.Register(&DesktopClickTool{base: base})
	r.Register(&DesktopTypeTool{base: base})
	r.Register(&DesktopKeyTool{base: base})
}
