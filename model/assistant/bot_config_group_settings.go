package assistant

import (
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"
)

// parseGroupSettings validates the complete patch before any configuration is saved.
func (t *dianaBotConfigTool) parseGroupSettings(raw any, scope string, group GroupConfig) (GroupConfig, error) {
	if scope != "group" {
		return group, fmt.Errorf("group_settings 只能修改当前群，请在目标群中操作")
	}
	input, ok := raw.(map[string]any)
	if !ok || len(input) == 0 {
		return group, fmt.Errorf("group_settings 必须是非空对象")
	}
	// Group stores may return shared maps. Never mutate them before persistence succeeds.
	group.PluginOverrides = maps.Clone(group.PluginOverrides)
	for key, value := range input {
		switch key {
		case "enabled":
			enabled, ok := value.(bool)
			if !ok {
				return group, fmt.Errorf("enabled 必须是布尔值")
			}
			group.Enabled, group.EnabledSet = enabled, true
		case "triggers":
			words, err := toolStringValues(value)
			if err != nil || value == nil {
				return group, fmt.Errorf("triggers 必须是文本数组，空数组恢复跟随机器人")
			}
			words = cleanStrings(words)
			if len(words) > 50 {
				return group, fmt.Errorf("触发词最多 50 个")
			}
			for _, word := range words {
				if utf8.RuneCountInString(word) > 100 {
					return group, fmt.Errorf("每个触发词最多 100 字")
				}
			}
			group.GroupTriggers = words
		case "trigger_mode":
			mode, ok := value.(string)
			if !ok {
				return group, fmt.Errorf("trigger_mode 必须是文本")
			}
			switch AliasTriggerMode(mode) {
			case "", AliasTriggerLoose, AliasTriggerSmart, AliasTriggerStrict:
			default:
				return group, fmt.Errorf("trigger_mode 必须是 loose、smart、strict 或空字符串（跟随机器人）")
			}
			group.GroupTriggerMode = AliasTriggerMode(mode)
		case "plugins":
			plugins, ok := value.(map[string]any)
			if !ok || len(plugins) == 0 {
				return group, fmt.Errorf("plugins 必须是非空对象，键为插件 ID，值为布尔值或 null")
			}
			if t.runtime.plugins == nil {
				return group, fmt.Errorf("插件管理器不可用")
			}
			if group.PluginOverrides == nil {
				group.PluginOverrides = map[string]bool{}
			}
			for id, raw := range plugins {
				state, exists := t.runtime.plugins.Get(id)
				if !exists || !state.Installed || state.Manifest.Internal || id == webSearchPluginID {
					return group, fmt.Errorf("插件 %q 不支持群级开关；先 get 查看可配置插件，联网搜索使用机器人级搜索配置", id)
				}
				if raw == nil {
					delete(group.PluginOverrides, id)
					continue
				}
				enabled, ok := raw.(bool)
				if !ok {
					return group, fmt.Errorf("插件 %q 开关必须是布尔值或 null", id)
				}
				if enabled && !pluginSupportsPlatform(state.Manifest, t.event.Platform) {
					return group, fmt.Errorf("插件 %q 不支持当前平台", id)
				}
				group.PluginOverrides[id] = enabled
			}
		default:
			return group, fmt.Errorf("不支持的群配置项 %q", key)
		}
	}
	return group, nil
}

func groupSettingsSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"enabled":      toolBoolParam("本群启用或停用；停用后行为沿用 WebUI 群设置。"),
		"triggers":     toolStringArrayParam("替换本群触发词，最多 50 个、每个 100 字；空数组恢复跟随机器人。"),
		"trigger_mode": toolEnumParam("触发词匹配模式；空字符串恢复继承。", "loose", "smart", "strict", ""),
		"plugins":      map[string]any{"type": "object", "description": "本群插件：键用 get 返回的 ID；true 开、false 关、null 继承。不支持联网搜索和内部能力。", "additionalProperties": map[string]any{"type": []string{"boolean", "null"}}},
	})
}

func (t *dianaBotConfigTool) groupSettingsView(scope string, group GroupConfig, effective BotConfig) any {
	if scope != "group" {
		return nil
	}
	plugins := []map[string]any{}
	if t.runtime.plugins != nil {
		overrides := t.runtime.pluginOverridesForEvent(t.event)
		for _, state := range t.runtime.plugins.ListVisibleForProfile(group.BotProfileID) {
			id := state.Manifest.ID
			if !state.Installed || id == webSearchPluginID {
				continue
			}
			var override any
			if value, exists := group.PluginOverrides[id]; exists {
				override = value
			}
			plugins = append(plugins, map[string]any{"id": id, "name": state.Manifest.Name, "enabled": t.runtime.plugins.EnabledWithOverrides(id, overrides), "group_override": override, "supported": pluginSupportsPlatform(state.Manifest, t.event.Platform)})
		}
	}
	return map[string]any{"enabled": group.Enabled, "triggers": effective.GroupTriggers, "trigger_mode": normalizeAliasTriggerMode(effective.GroupTriggerMode), "trigger_overrides": group.GroupTriggers, "trigger_mode_override": strings.TrimSpace(string(group.GroupTriggerMode)), "plugins": plugins}
}
