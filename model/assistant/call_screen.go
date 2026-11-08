// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/desktopctl"
	"github.com/SuInk/diana/model/llm"
)

// call_screen 让模型操作通话容器里的客户端界面：列窗口、截图、读元素、点击、输入、按键、
// 滚动。截图直接附给模型看；读元素给出带编号的文字行和红绿圆按钮，点击时带 element_id
// 和 observation 比估坐标准。只给主人，且要在通话插件里开着「允许机器人操作容器屏幕」。

const callScreenToolName = "call_screen"

type callScreenTool struct {
	runtime *Runtime
	event   MessageEvent

	mu    sync.Mutex
	parts []llm.ContentPart
}

func (t *callScreenTool) Name() string { return callScreenToolName }

func (t *callScreenTool) Description() string {
	return "操作通话容器里的聊天客户端界面（不是本机桌面），拨电话时配合 phone_call 用。action：windows 列窗口；screenshot 截图给你看；" +
		"elements 读出带编号的文字和按钮；click 点击（element_id+observation，或窗口内 x、y）；type 输入文字；key 按键（Return、Escape、ctrl+a）；scroll 滚动。" +
		"每点一步先截图或读元素确认画面。"
}

func (t *callScreenTool) RepeatableCalls() bool { return true }

func (t *callScreenTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action":      map[string]any{"type": "string", "enum": []string{"windows", "screenshot", "elements", "click", "type", "key", "scroll"}},
		"window_id":   toolStringParam("窗口 ID，来自 windows；除 windows 外都要填"),
		"element_id":  toolStringParam("click 时点哪个元素，来自 elements"),
		"observation": map[string]any{"type": "integer", "description": "element_id 所属那次 elements 返回的 observation"},
		"x":           map[string]any{"type": "number", "description": "click/scroll 的窗口内横坐标"},
		"y":           map[string]any{"type": "number", "description": "click/scroll 的窗口内纵坐标"},
		"text":        toolStringParam("type 要输入的文字"),
		"key":         toolStringParam("key 要按的键"),
		"delta_y":     map[string]any{"type": "integer", "description": "scroll 纵向像素，正数向下"},
	})
}

func (t *callScreenTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}

var _ agent.ToolResultPartsTool = (*callScreenTool)(nil)

var callScreenOps = map[string]string{
	"windows":    desktopctl.OpWindowsList,
	"screenshot": desktopctl.OpWindowScreenshot,
	"elements":   desktopctl.OpWindowElements,
	"click":      desktopctl.OpWindowClick,
	"type":       desktopctl.OpWindowType,
	"key":        desktopctl.OpWindowKey,
	"scroll":     desktopctl.OpWindowScroll,
}

func (t *callScreenTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	t.parts = nil
	t.mu.Unlock()
	op, ok := callScreenOps[strings.TrimSpace(configToolString(input, "action"))]
	if !ok {
		return "", errors.New("action 只能是 windows、screenshot、elements、click、type、key、scroll")
	}
	calls, err := callScreenBridge(t.runtime, t.event)
	if err != nil {
		return "", err
	}
	cmd := desktopctl.Command{
		WindowID:    strings.TrimSpace(configToolString(input, "window_id")),
		ElementID:   strings.TrimSpace(configToolString(input, "element_id")),
		Observation: int64(intFromAny(input["observation"])),
		Text:        configToolString(input, "text"),
		Key:         strings.TrimSpace(configToolString(input, "key")),
		DeltaY:      intFromAny(input["delta_y"]),
	}
	if x, ok := floatFromAny(input["x"]); ok {
		cmd.X = &x
	}
	if y, ok := floatFromAny(input["y"]); ok {
		cmd.Y = &y
	}
	var raw json.RawMessage
	if err := calls.desktop.call(ctx, op, cmd, &raw); err != nil {
		return "", err
	}
	if op != desktopctl.OpWindowScreenshot {
		return string(raw), nil
	}
	var shot desktopctl.ScreenshotPayload
	if err := json.Unmarshal(raw, &shot); err != nil || shot.Data == "" {
		return "", errors.New("截图没拿到")
	}
	t.mu.Lock()
	t.parts = []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64," + shot.Data}}
	t.mu.Unlock()
	body, err := json.Marshal(map[string]any{"window_id": shot.WindowID, "title": shot.Title, "observation": shot.Observation, "note": "截图已附在这条结果里"})
	return string(body), err
}

func floatFromAny(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// callScreenBridge 取能操作屏幕的通话连接：插件开着、开了屏幕操作、连着 agent。
func callScreenBridge(r *Runtime, event MessageEvent) (*bridgeCallSet, error) {
	if r == nil || r.plugins == nil {
		return nil, errors.New("通话插件没开")
	}
	plugin, settings, enabled := r.plugins.PluginWithSettingsForProfile(callPluginID, event.ProfileID)
	bridge, ok := plugin.(*CallPlugin)
	if !enabled || !ok {
		return nil, errors.New("通话插件没开")
	}
	if !settings.Bool(callPluginSettingScreen, true) {
		return nil, errors.New("通话插件设置里关了「允许机器人操作容器屏幕」")
	}
	calls := bridge.currentCalls()
	if calls == nil || calls.currentLink() == nil {
		return nil, errors.New("没连上容器里的通话插件")
	}
	return calls, nil
}
