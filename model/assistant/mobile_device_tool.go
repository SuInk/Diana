// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/mobilectl"
)

const mobileDeviceToolName = "mobile_device"

type mobileDeviceTool struct {
	runtime *Runtime
	event   MessageEvent

	mu    sync.Mutex
	parts []llm.ContentPart
}

func (t *mobileDeviceTool) Name() string { return mobileDeviceToolName }

func (t *mobileDeviceTool) Description() string {
	return "控制主人的安卓或 iPhone 手机。action：devices 列设备；open_app 打开应用（x、xiaohongshu、heybox 或包名）；" +
		"open_url 打开链接；screenshot 截屏给你看；elements 读带编号的界面元素；tap 点击（element_id+observation 或 x、y）；" +
		"swipe 滑动（up/down/left/right）；type 输入文字；key 按键（home/back/enter）；patrol_now 立即巡检一次热闻。"
}

func (t *mobileDeviceTool) RepeatableCalls() bool { return true }

func (t *mobileDeviceTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action": map[string]any{
			"type": "string",
			"enum": []string{"devices", "open_app", "open_url", "screenshot", "elements", "tap", "swipe", "type", "key", "patrol_now"},
		},
		"device_id":   toolStringParam("手机设备 ID；只有一台在线手机时可留空"),
		"app":         toolStringParam("open_app 要打开的应用（支持 x、xiaohongshu、heybox 或包名/BundleID）"),
		"url":         toolStringParam("open_url 要打开的链接或 Scheme"),
		"element_id":  map[string]any{"type": "integer", "description": "tap 要点击的元素编号，来自 elements"},
		"observation": map[string]any{"type": "integer", "description": "element_id 对应的 elements 返回的 observation"},
		"x":           map[string]any{"type": "integer", "description": "tap 的屏幕横坐标"},
		"y":           map[string]any{"type": "integer", "description": "tap 的屏幕纵坐标"},
		"direction":   toolStringParam("swipe 滑动方向：up、down、left、right"),
		"text":        toolStringParam("type 要输入的文字"),
		"key":         toolStringParam("key 要按的系统键：home、back、enter"),
	})
}

func (t *mobileDeviceTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}

var _ agent.ToolResultPartsTool = (*mobileDeviceTool)(nil)

func (t *mobileDeviceTool) activePlugin() (*MobileSocialPlugin, *mobilectl.Controller, error) {
	if t.runtime == nil || t.runtime.plugins == nil {
		return nil, nil, errors.New("手机控制与热闻巡检插件未启用")
	}
	pluginVal, settings, enabled := t.runtime.pluginWithSettingsForEvent(mobileSocialPluginID, t.event)
	plugin, ok := pluginVal.(*MobileSocialPlugin)
	if !enabled || !ok || plugin == nil {
		return nil, nil, errors.New("手机控制与热闻巡检插件未启用")
	}
	if !settings.Bool(mobileSocialSettingAllowAgent, true) {
		return nil, nil, errors.New("插件设置里已关闭「允许主人在聊天中操控手机」")
	}
	ctrl := plugin.activeController()
	if ctrl == nil {
		return nil, nil, errors.New("手机控制驱动未初始化")
	}
	return plugin, ctrl, nil
}

func (t *mobileDeviceTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	t.parts = nil
	t.mu.Unlock()

	plugin, ctrl, err := t.activePlugin()
	if err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(configToolString(input, "action")))
	deviceID := strings.TrimSpace(configToolString(input, "device_id"))

	if action == "patrol_now" {
		res, patrolErr := plugin.PatrolOnce(ctx)
		if patrolErr != nil && res.FetchedCount == 0 {
			return "", patrolErr
		}
		raw, err := json.Marshal(res)
		return string(raw), err
	}

	var resultJSON string
	runErr := ctrl.WithExclusiveSession(ctx, func(ctx context.Context) error {
		switch action {
		case "devices":
			devs, err := ctrl.ListDevices(ctx)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"devices": devs})
			resultJSON = string(raw)
			return nil

		case "open_app":
			appArg := strings.TrimSpace(configToolString(input, "app"))
			if appArg == "" {
				return errors.New("open_app 需要提供 app 参数")
			}
			dev, err := ctrl.ResolveDevice(ctx, deviceID)
			if err != nil {
				return err
			}
			pkg, fallbackURL := ResolveMobileAppIdentifier(dev.Platform, appArg)
			if _, err := ctrl.LaunchApp(ctx, dev.ID, pkg); err != nil {
				if fallbackURL == "" {
					return err
				}
				if _, urlErr := ctrl.OpenURL(ctx, dev.ID, fallbackURL); urlErr != nil {
					return err
				}
			}
			raw, _ := json.Marshal(map[string]any{"device_id": dev.ID, "platform": dev.Platform, "opened": pkg})
			resultJSON = string(raw)
			return nil

		case "open_url":
			rawURL := strings.TrimSpace(configToolString(input, "url"))
			if rawURL == "" {
				return errors.New("open_url 需要提供 url 参数")
			}
			dev, err := ctrl.OpenURL(ctx, deviceID, rawURL)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"device_id": dev.ID, "url": rawURL})
			resultJSON = string(raw)
			return nil

		case "screenshot":
			dev, pngBytes, err := ctrl.Screenshot(ctx, deviceID)
			if err != nil {
				return err
			}
			b64 := base64.StdEncoding.EncodeToString(pngBytes)
			t.mu.Lock()
			t.parts = []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64," + b64}}
			t.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{
				"device_id": dev.ID,
				"platform":  dev.Platform,
				"note":      "手机截图已附在本条工具结果中",
			})
			resultJSON = string(raw)
			return nil

		case "elements":
			snap, err := ctrl.Elements(ctx, deviceID)
			if err != nil {
				return err
			}
			if len(snap.Elements) > 80 {
				snap.Elements = snap.Elements[:80]
			}
			raw, _ := json.Marshal(snap)
			resultJSON = string(raw)
			return nil

		case "tap":
			elemIdx := intFromAny(input["element_id"])
			obs := int64(intFromAny(input["observation"]))
			if elemIdx > 0 {
				dev, elem, err := ctrl.TapElement(ctx, deviceID, obs, elemIdx)
				if err != nil {
					return err
				}
				cx, cy := elem.Center()
				raw, _ := json.Marshal(map[string]any{
					"device_id": dev.ID,
					"tapped":    elem.DisplayText(),
					"x":         cx,
					"y":         cy,
				})
				resultJSON = string(raw)
				return nil
			}
			x, hasX := floatFromAny(input["x"])
			y, hasY := floatFromAny(input["y"])
			if !hasX || !hasY {
				return errors.New("tap 需要提供 element_id（配合 observation）或 x、y 坐标")
			}
			dev, err := ctrl.Tap(ctx, deviceID, int(x+0.5), int(y+0.5))
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"device_id": dev.ID, "x": int(x + 0.5), "y": int(y + 0.5)})
			resultJSON = string(raw)
			return nil

		case "swipe":
			dir := firstNonEmpty(strings.TrimSpace(configToolString(input, "direction")), "up")
			dev, err := ctrl.SwipeDirection(ctx, deviceID, dir)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"device_id": dev.ID, "direction": dir})
			resultJSON = string(raw)
			return nil

		case "type":
			txt := configToolString(input, "text")
			if txt == "" {
				return errors.New("type 需要提供 text 参数")
			}
			dev, err := ctrl.TypeText(ctx, deviceID, txt)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"device_id": dev.ID, "typed_runes": strconv.Itoa(len([]rune(txt)))})
			resultJSON = string(raw)
			return nil

		case "key":
			k := firstNonEmpty(strings.TrimSpace(configToolString(input, "key")), "home")
			dev, err := ctrl.PressKey(ctx, deviceID, k)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"device_id": dev.ID, "key": k})
			resultJSON = string(raw)
			return nil

		default:
			return errors.New("不支持的 action")
		}
	})
	if runErr != nil {
		return "", runErr
	}
	return resultJSON, nil
}
