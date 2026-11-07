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
		out["browser_source"] = map[string]any{"actions": []string{"update"}, "fields": map[string]string{"order": "[]string", "box_enabled": "bool", "extension_enabled": "bool", "sandbox": "string"}, "hint": "浏览器来源：内置浏览器/浏览器扩展各自的开关和优先顺序（order 取 box、extension）；sandbox 是所有 Diana 拉起的 Chromium 的进程沙箱档位：sandbox 仅沙箱（默认，起不来就报错）、auto 自动（沙箱起不来时关闭；root 运行时浏览器自动改用 diana 用户启动，不用为 root 换档）、off 关闭。关闭沙箱会削弱对群成员发来网页的隔离，改之前向主人说明。"}
	}
	if s.BrowserControl != nil {
		out["browser_control"] = map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(s.BrowserControl.registry.Policy()), "hint": "浏览器控制扩展的总开关、站点白名单/黑名单和写操作档位；数组按完整列表替换。关掉总开关会立即断开已连接的扩展。"}
	}
	if s.DesktopControl != nil {
		out["desktop_control"] = map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(s.DesktopControl.registry.Policy()), "hint": "桌面控制：让机器人查看并操作运行 Diana 的这台 Mac 上的窗口（仅 macOS，仅主人）。enabled 是总开关，write_enabled 允许点击、输入、按键和滚动（打开后直接执行，不逐条确认），allowed_apps/denied_apps 收窄应用范围（空白名单=全部应用）。主人要你操作电脑时，还要把 bot 资源的 agent_desktop_control_enabled 打开，两边都开、下一条消息起才有 desktop_* 工具。首次打开会自动下载执行器；屏幕录制和辅助功能权限要主人在 macOS 系统设置里授权。改动会中断正在执行的桌面任务并重连执行器。"}
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
			if key != "order" && key != "box_enabled" && key != "extension_enabled" && key != "sandbox" {
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
