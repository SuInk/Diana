package webui

import (
	"context"
	"fmt"
	"net/url"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/updater"
	"github.com/gin-gonic/gin"
)

// 控制台上不属于某台机器人的全局开关（浏览器来源、浏览器控制、桌面控制、内置浏览器、
// 自动更新、群风格学习）。和机器人设置一样走 console_config 的暂存→确认，复用 WebUI
// 同一个 handler，所以校验、落库和副作用（关总开关当场断开连接等）与页面上点按钮一致。
// 主人在私聊里能改的就是控制台能改的全部。

// ConsoleSwitchHandlers 是这些全局开关各自页面的 handler；缺哪个就不列哪个资源。
type ConsoleSwitchHandlers struct {
	BrowserSource  *BrowserSourceHandler
	BrowserControl *BrowserControlHandler
	DesktopControl *DesktopControlHandler
	BrowserBox     *BrowserBoxHandler
	SystemUpdate   *SystemUpdateHandler
}

// SetSwitchHandlers 注入全局开关的 handler。
func (m *ConsoleConfigManager) SetSwitchHandlers(handlers ConsoleSwitchHandlers) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.switches = handlers
}

func (m *ConsoleConfigManager) switchCatalog(out map[string]any) {
	s := m.switches
	if s.BrowserSource != nil {
		out["browser_source"] = map[string]any{"actions": []string{"update"}, "fields": map[string]string{"order": "[]string", "box_enabled": "bool", "extension_enabled": "bool"}, "hint": "浏览器来源：内置浏览器/浏览器扩展各自的开关和优先顺序（order 取 box、extension）。"}
	}
	if s.BrowserControl != nil {
		out["browser_control"] = map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(s.BrowserControl.registry.Policy()), "hint": "浏览器控制扩展的总开关、站点白名单/黑名单和写操作档位；数组按完整列表替换。关掉总开关会立即断开已连接的扩展。"}
	}
	if s.DesktopControl != nil {
		out["desktop_control"] = map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(s.DesktopControl.registry.Policy()), "hint": "桌面控制总开关、应用白名单/黑名单、写操作和限速；改动会中断正在执行的桌面任务并重连执行器。"}
	}
	if s.BrowserBox != nil {
		out["browser_box"] = map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(s.BrowserBox.manager.Settings()), "hint": "内置常驻浏览器：enabled 关掉会当场结束进程。"}
	}
	if s.SystemUpdate != nil {
		out["update_policy"] = map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(updater.UpdatePolicy{}), "hint": "更新通道 release/beta/canary，自动下载、自动安装、Docker 自动更新和 GitHub 下载线路。"}
	}
	out["group_style"] = map[string]any{"actions": []string{"update"}, "fields": map[string]string{"enabled": "bool"}, "hint": "target 为群 ID；开关这个群的说话风格学习。机器人级总开关是 bot 的 expression_learning_enabled。"}
}

// readSwitch 返回 handled=false 表示不是全局开关资源。
func (m *ConsoleConfigManager) readSwitch(ctx context.Context, profile, resource, target string) (any, bool, error) {
	s := m.switches
	switch resource {
	case "browser_source":
		if s.BrowserSource == nil {
			return nil, true, errSwitchUnavailable
		}
		return secretmask.ChatConfigValue(s.BrowserSource.state()), true, nil
	case "browser_control":
		if s.BrowserControl == nil {
			return nil, true, errSwitchUnavailable
		}
		return secretmask.ChatConfigValue(s.BrowserControl.registry.Policy()), true, nil
	case "desktop_control":
		if s.DesktopControl == nil {
			return nil, true, errSwitchUnavailable
		}
		return secretmask.ChatConfigValue(s.DesktopControl.registry.Policy()), true, nil
	case "browser_box":
		if s.BrowserBox == nil {
			return nil, true, errSwitchUnavailable
		}
		return secretmask.ChatConfigValue(s.BrowserBox.manager.Settings()), true, nil
	case "update_policy":
		if s.SystemUpdate == nil {
			return nil, true, errSwitchUnavailable
		}
		return secretmask.ChatConfigValue(s.SystemUpdate.currentPolicy()), true, nil
	case "group_style":
		if target == "" {
			return nil, true, fmt.Errorf("请指定群 ID")
		}
		out, err := m.invokeWithQuery(ctx, profile, target, nil, m.bot.getGroupStyle, url.Values{"bot_profile_id": {profile}})
		return out, true, err
	}
	return nil, false, nil
}

// applySwitch 把补丁合并到当前值再交给页面同一个 handler 保存。
func (m *ConsoleConfigManager) applySwitch(ctx context.Context, profile, resource, action, target string, payload map[string]any) (any, bool, error) {
	s := m.switches
	var current any
	var handler gin.HandlerFunc
	switch resource {
	case "browser_source":
		if s.BrowserSource == nil {
			return nil, true, errSwitchUnavailable
		}
		// 这个接口本身就是补丁：只带要改的键。
		for key := range payload {
			if key != "order" && key != "box_enabled" && key != "extension_enabled" {
				return nil, true, fmt.Errorf("配置项 %s 不存在或只读", key)
			}
		}
		if action != "update" {
			return nil, true, errSwitchAction
		}
		out, err := m.invoke(ctx, profile, target, payload, s.BrowserSource.set)
		return out, true, err
	case "browser_control":
		if s.BrowserControl == nil {
			return nil, true, errSwitchUnavailable
		}
		current, handler = s.BrowserControl.registry.Policy(), s.BrowserControl.setPolicy
	case "desktop_control":
		if s.DesktopControl == nil {
			return nil, true, errSwitchUnavailable
		}
		current, handler = s.DesktopControl.registry.Policy(), s.DesktopControl.policy
	case "browser_box":
		if s.BrowserBox == nil {
			return nil, true, errSwitchUnavailable
		}
		current, handler = s.BrowserBox.manager.Settings(), s.BrowserBox.setSettings
	case "update_policy":
		if s.SystemUpdate == nil {
			return nil, true, errSwitchUnavailable
		}
		current, handler = s.SystemUpdate.currentPolicy(), s.SystemUpdate.savePolicy
	case "group_style":
		if action != "update" || target == "" {
			return nil, true, fmt.Errorf("请指定群 ID 并使用 update")
		}
		enabled, ok := payload["enabled"].(bool)
		if !ok || len(payload) != 1 {
			return nil, true, fmt.Errorf("group_style 只接受布尔值 enabled")
		}
		out, err := m.invokeWithQuery(ctx, profile, target, map[string]any{"enabled": enabled}, m.bot.setGroupStyleEnabled, url.Values{"bot_profile_id": {profile}})
		return out, true, err
	default:
		return nil, false, nil
	}
	if action != "update" {
		return nil, true, errSwitchAction
	}
	merged, err := consoleMerge(current, payload)
	if err != nil {
		return nil, true, err
	}
	out, err := m.invoke(ctx, profile, target, merged, handler)
	return out, true, err
}

var (
	errSwitchUnavailable = fmt.Errorf("当前部署没有这项设置")
	errSwitchAction      = fmt.Errorf("这项设置只支持 update")
)
