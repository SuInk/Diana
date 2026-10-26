// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 主人在聊天里开关插件：机器人级开关，和插件页「当前机器人」那一栏是同一份数据。
// 群级覆盖仍由 bot_config 的 group_settings 管，这里只报告它盖没盖住机器人级开关。

const pluginManageToolName = "plugin_manage"

// SetPluginStatePersister 设置插件开关的落盘函数。插件状态不在机器人配置里，WebUI
// 自己落盘；聊天里改开关要靠这个钩子，不设时改动只在内存里，重启丢失。
func (r *Runtime) SetPluginStatePersister(persist func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pluginStatePersister = persist
}

// resolvePluginID 按 ID 或名称找插件，大小写不敏感。内化插件不出现在插件页，也不让
// 在聊天里点名。
func (r *Runtime) resolvePluginID(name string) (PluginState, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return PluginState{}, fmt.Errorf("请给出插件 ID 或名称")
	}
	if r == nil || r.plugins == nil {
		return PluginState{}, fmt.Errorf("插件管理器不可用")
	}
	var byName []PluginState
	for _, state := range r.plugins.ListVisible() {
		if strings.EqualFold(state.Manifest.ID, name) {
			return state, nil
		}
		if strings.EqualFold(state.Manifest.Name, name) {
			byName = append(byName, state)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return PluginState{}, fmt.Errorf("没有找到插件「%s」，发送「插件 列表」查看 ID", name)
	default:
		return PluginState{}, fmt.Errorf("有多个插件叫「%s」，请改用插件 ID", name)
	}
}

// setPluginEnabledForEvent 改事件所属机器人的插件开关并落盘。
func (r *Runtime) setPluginEnabledForEvent(ctx context.Context, event MessageEvent, name string, enabled bool) (PluginState, error) {
	state, err := r.resolvePluginID(name)
	if err != nil {
		return PluginState{}, err
	}
	if !state.Installed {
		return PluginState{}, fmt.Errorf("插件「%s」未安装，请在 WebUI 插件页安装", state.Manifest.Name)
	}
	profileID := r.eventProfileID(event)
	updated, err := r.plugins.SetEnabledForProfile(state.Manifest.ID, profileID, enabled)
	if err != nil {
		return PluginState{}, err
	}
	r.mu.RLock()
	persist := r.pluginStatePersister
	r.mu.RUnlock()
	if persist != nil {
		if err := persist(ctx); err != nil {
			return updated, fmt.Errorf("开关已生效，但保存失败，重启后会恢复原状：%w", err)
		}
	}
	return updated, nil
}

type pluginListItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	// Effective 是这个会话实际生效的开关：群级覆盖或机器人停用会盖住机器人级开关。
	Effective bool `json:"effective"`
}

func (r *Runtime) pluginListForEvent(event MessageEvent) []pluginListItem {
	if r == nil || r.plugins == nil {
		return nil
	}
	overrides := r.pluginOverridesForEvent(event)
	states := r.plugins.ListVisibleForProfile(r.eventProfileID(event))
	out := make([]pluginListItem, 0, len(states))
	for _, state := range states {
		out = append(out, pluginListItem{
			ID:        state.Manifest.ID,
			Name:      state.Manifest.Name,
			Installed: state.Installed,
			Enabled:   state.Enabled,
			Effective: r.plugins.EnabledWithOverrides(state.Manifest.ID, overrides),
		})
	}
	return out
}

func (r *Runtime) renderPluginList(event MessageEvent) string {
	items := r.pluginListForEvent(event)
	if len(items) == 0 {
		return "当前没有可管理的插件。"
	}
	var builder strings.Builder
	builder.WriteString("插件（当前机器人）：")
	for _, item := range items {
		status := "已停用"
		switch {
		case !item.Installed:
			status = "未安装"
		case item.Enabled && !item.Effective:
			status = "已启用（本会话被覆盖为停用）"
		case !item.Enabled && item.Effective:
			status = "已停用（本会话被覆盖为启用）"
		case item.Enabled:
			status = "已启用"
		}
		builder.WriteString("\n" + item.Name + "（" + item.ID + "）：" + status)
	}
	builder.WriteString("\n用「插件 启用 <ID>」「插件 禁用 <ID>」切换。")
	return builder.String()
}

func runOwnerPluginToggle(enabled bool) func(*Runtime, MessageEvent, string) string {
	return func(r *Runtime, event MessageEvent, name string) string {
		state, err := r.setPluginEnabledForEvent(context.Background(), event, name, enabled)
		if err != nil {
			if errors.Is(err, ErrInternalPluginDisable) {
				return "这个插件是内置能力，不能停用。"
			}
			if state.Manifest.ID == "" {
				return "切换插件失败：" + err.Error()
			}
			return err.Error()
		}
		action := "已停用"
		if enabled {
			action = "已启用"
		}
		reply := action + "插件「" + state.Manifest.Name + "」（当前机器人）。"
		if r.plugins.EnabledWithOverrides(state.Manifest.ID, r.pluginOverridesForEvent(event)) != enabled {
			reply += "本群有单独的插件覆盖，本群实际状态没变，可在群设置里调整。"
		}
		return reply
	}
}

type dianaPluginManageTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (*dianaPluginManageTool) Name() string { return pluginManageToolName }
func (*dianaPluginManageTool) Description() string {
	return "主人管理当前机器人的插件开关：list 列出插件及启用状态，enable/disable 按插件 ID 或名称切换机器人级开关并保存。只影响当前机器人；单个群的覆盖开关用 bot_config 的 group_settings。插件安装、卸载和详细参数在 WebUI 插件页。"
}
func (*dianaPluginManageTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("list 列出；enable 启用；disable 停用", "list", "enable", "disable"),
		"plugin":    toolStringParam("enable/disable 的插件 ID 或名称"),
	})
}

func (t *dianaPluginManageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	if !t.runtime.effectiveConfigForEvent(t.event).IsOwnerEvent(t.event) && !isAdminChatRun(ctx) {
		return "", fmt.Errorf("只有主人可以管理插件")
	}
	switch op := configToolString(input, "operation"); op {
	case "list":
		return hostAccessJSON(map[string]any{"plugins": t.runtime.pluginListForEvent(t.event)})
	case "enable", "disable":
		state, err := t.runtime.setPluginEnabledForEvent(ctx, t.event, configToolString(input, "plugin"), op == "enable")
		if err != nil {
			return "", err
		}
		effective := t.runtime.plugins.EnabledWithOverrides(state.Manifest.ID, t.runtime.pluginOverridesForEvent(t.event))
		return hostAccessJSON(map[string]any{"ok": true, "id": state.Manifest.ID, "name": state.Manifest.Name, "enabled": state.Enabled, "effective_here": effective})
	default:
		return "", fmt.Errorf("operation 必须为 list、enable 或 disable")
	}
}
