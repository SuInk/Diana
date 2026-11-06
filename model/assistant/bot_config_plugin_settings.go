package assistant

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
)

// 插件详细设置（链接解析平台、翻译开关、音乐源……）和 WebUI 插件页是同一份数据：
// 群级写 GroupConfig.PluginSettingOverrides，机器人级写插件全局设置。凭据类设置
// 经 console_config 暂存确认，不从这个即时保存入口修改。

func pluginSettingsSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "插件详细设置 {插件ID:{键:值}}，插件 ID 也可写插件名称或它提供的工具名；null 恢复继承；先 get 带 plugin 查键，凭据通过 console_config 暂存确认。",
		"additionalProperties": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{},
		},
	}
}

// pluginSettingEditable 拒绝凭据和不允许按该层级/身份修改的键。
func pluginSettingEditable(spec PluginSettingSpec, scope string, owner bool) error {
	if spec.Secret {
		return fmt.Errorf("设置 %q 是凭据，请用 console_config 暂存并确认", spec.Key)
	}
	if scope == PluginSettingScopeGroup && !spec.allowsScope(PluginSettingScopeGroup) {
		return fmt.Errorf("设置 %q 只能在机器人级修改，主人可带 scope=bot 再改", spec.Key)
	}
	editor := PluginSettingEditorGroupAdmin
	if owner {
		editor = PluginSettingEditorOwner
	}
	if !spec.CanConfigure(scope, editor) {
		return fmt.Errorf("设置 %q 需要机器人主人修改", spec.Key)
	}
	return nil
}

func (t *dianaBotConfigTool) configurablePlugin(id string) (PluginState, error) {
	if t.runtime.plugins == nil {
		return PluginState{}, fmt.Errorf("插件管理器不可用")
	}
	state, err := t.runtime.resolveInstalledPlugin(t.event, id)
	if err != nil {
		return PluginState{}, err
	}
	switch {
	case state.Manifest.ID == webSearchPluginID:
		return PluginState{}, fmt.Errorf("联网搜索的设置在统一搜索配置里，请用 console_config 查看和修改")
	case state.Manifest.Internal:
		return PluginState{}, fmt.Errorf("%q 是内置能力，没有单独的插件设置", state.Manifest.ID)
	case len(state.Manifest.Settings) == 0:
		return PluginState{}, fmt.Errorf("插件 %q 没有可配置的设置", state.Manifest.ID)
	}
	return state, nil
}

// resolveInstalledPlugin 把模型给的名字解析成已安装的插件。模型常拿它用过的工具名
// （github）或插件名称去问，而不是 official.repository-publish 这种 ID；只认 ID
// 时它会被一句「不可配置」劝退，转头让主人去控制台。所以这里依次认：ID、ID 末段、
// 插件名称、插件贡献的工具名，唯一命中才算。
func (r *Runtime) resolveInstalledPlugin(event MessageEvent, name string) (PluginState, error) {
	if r == nil || r.plugins == nil {
		return PluginState{}, fmt.Errorf("插件管理器不可用")
	}
	name = strings.TrimSpace(name)
	if state, ok := r.plugins.Get(name); ok && state.Installed {
		return state, nil
	}
	want := strings.ToLower(name)
	states := r.plugins.List()
	// 关着的插件也要认得出工具名：主人常常就是想先改好设置再打开。
	allOn := make(map[string]bool, len(states))
	for _, state := range states {
		allOn[state.Manifest.ID] = true
	}
	owners := r.plugins.AgentToolOwners(r.currentPlatform(event), allOn, r.pluginSettingOverridesForEvent(event))
	var matched []PluginState
	var listed []string
	for _, state := range states {
		if !state.Installed {
			continue
		}
		id := state.Manifest.ID
		if !state.Manifest.Internal && len(state.Manifest.Settings) > 0 {
			listed = append(listed, id+"（"+state.Manifest.Name+"）")
		}
		hit := strings.ToLower(id) == want || strings.ToLower(state.Manifest.Name) == want
		if _, tail, ok := strings.Cut(id, "."); ok && strings.ToLower(tail) == want {
			hit = true
		}
		for _, tool := range owners[id] {
			if strings.ToLower(tool) == want {
				hit = true
			}
		}
		if hit {
			matched = append(matched, state)
		}
	}
	if len(matched) == 1 {
		return matched[0], nil
	}
	if len(matched) > 1 {
		ids := make([]string, 0, len(matched))
		for _, state := range matched {
			ids = append(ids, state.Manifest.ID)
		}
		return PluginState{}, fmt.Errorf("%q 对应多个插件：%s，请改用插件 ID", name, strings.Join(ids, "、"))
	}
	return PluginState{}, fmt.Errorf("没有插件 %q；可配置的插件：%s", name, strings.Join(listed, "、"))
}

type pluginSettingsPatch map[string]map[string]any

func parsePluginSettingsPatch(raw any) (pluginSettingsPatch, error) {
	input, ok := raw.(map[string]any)
	if !ok || len(input) == 0 {
		return nil, fmt.Errorf("plugin_settings 必须是非空对象，键为插件 ID")
	}
	out := pluginSettingsPatch{}
	for id, values := range input {
		fields, ok := values.(map[string]any)
		if !ok || len(fields) == 0 {
			return nil, fmt.Errorf("插件 %q 的设置必须是非空对象", id)
		}
		out[id] = fields
	}
	return out, nil
}

// applyGroupPluginSettings 合并到群覆盖，整体校验通过才返回；不写存储。
func (t *dianaBotConfigTool) applyGroupPluginSettings(patch pluginSettingsPatch, owner bool, group GroupConfig) (GroupConfig, error) {
	next := PluginSettingOverrides{}
	for id, values := range group.PluginSettingOverrides {
		next[id] = maps.Clone(values)
	}
	for id, fields := range patch {
		state, err := t.configurablePlugin(id)
		if err != nil {
			return group, err
		}
		id = state.Manifest.ID
		values := next[id]
		if values == nil {
			values = map[string]any{}
		}
		for key, value := range fields {
			spec, ok := findPluginSettingSpec(state.Manifest.Settings, key)
			if !ok {
				return group, fmt.Errorf("插件 %q 没有设置 %q", id, key)
			}
			if err := pluginSettingEditable(spec, PluginSettingScopeGroup, owner); err != nil {
				return group, err
			}
			if value == nil {
				delete(values, key)
			} else {
				values[key] = value
			}
		}
		next[id] = values
	}
	normalized, err := t.runtime.plugins.ValidateGroupSettingOverrides(next)
	if err != nil {
		return group, err
	}
	group.PluginSettingOverrides = normalized
	return group, nil
}

// prepareBotPluginSettings 先算好每个插件的新设置并校验，保存放到最后一步，
// 避免一个插件写进去了、另一个报错。
func (t *dianaBotConfigTool) prepareBotPluginSettings(patch pluginSettingsPatch) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for id, fields := range patch {
		state, err := t.configurablePlugin(id)
		if err != nil {
			return nil, err
		}
		id = state.Manifest.ID
		secrets := secretSettingKeys(state.Manifest.Settings)
		merged := map[string]any{}
		for key, value := range state.Settings {
			if !secrets[key] {
				merged[key] = value
			}
		}
		for key, value := range fields {
			spec, ok := findPluginSettingSpec(state.Manifest.Settings, key)
			if !ok {
				return nil, fmt.Errorf("插件 %q 没有设置 %q", id, key)
			}
			if err := pluginSettingEditable(spec, PluginSettingScopeBot, true); err != nil {
				return nil, err
			}
			if value == nil {
				delete(merged, key)
			} else {
				merged[key] = value
			}
		}
		if _, err := normalizePluginSettings(state.Manifest.Settings, merged); err != nil {
			return nil, fmt.Errorf("插件 %q：%w", id, err)
		}
		out[id] = merged
	}
	return out, nil
}

// saveBotPluginSettings 写入插件全局设置并落盘。凭据键不在 merged 里，
// UpdateSettingsForProfile 会保留已存的凭据。
func (t *dianaBotConfigTool) saveBotPluginSettings(ctx context.Context, prepared map[string]map[string]any) error {
	ids := make([]string, 0, len(prepared))
	for id := range prepared {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	profileID := t.runtime.eventProfileID(t.event)
	for _, id := range ids {
		if _, err := t.runtime.plugins.UpdateSettingsForProfile(id, profileID, prepared[id], nil); err != nil {
			return fmt.Errorf("插件 %q：%w", id, err)
		}
	}
	t.runtime.mu.RLock()
	persist := t.runtime.pluginStatePersister
	t.runtime.mu.RUnlock()
	if persist != nil {
		if err := persist(ctx); err != nil {
			return fmt.Errorf("插件设置已生效，但保存失败，重启后会恢复原状：%w", err)
		}
	}
	return nil
}

func findPluginSettingSpec(specs []PluginSettingSpec, key string) (PluginSettingSpec, bool) {
	for _, spec := range specs {
		if spec.Key == key {
			return spec, true
		}
	}
	return PluginSettingSpec{}, false
}

// pluginSettingsView 列出一个插件可在聊天里改的设置及当前生效值，凭据只报是否已配置。
func (t *dianaBotConfigTool) pluginSettingsView(id, scope string, owner bool, group GroupConfig) (any, error) {
	state, err := t.configurablePlugin(id)
	if err != nil {
		return nil, err
	}
	overrides := map[string]any(nil)
	if scope == PluginSettingScopeGroup {
		overrides = group.PluginSettingOverrides[state.Manifest.ID]
	}
	effective := effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, overrides)
	settings := make([]map[string]any, 0, len(state.Manifest.Settings))
	for _, spec := range state.Manifest.Settings {
		item := map[string]any{"key": spec.Key, "label": spec.Label, "type": spec.Type}
		if spec.Description != "" {
			item["description"] = spec.Description
		}
		if len(spec.Options) > 0 {
			options := make([]string, 0, len(spec.Options))
			for _, option := range spec.Options {
				options = append(options, option.Value+"="+option.Label)
			}
			item["options"] = options
		}
		if spec.Min != nil {
			item["min"] = *spec.Min
		}
		if spec.Max != nil {
			item["max"] = *spec.Max
		}
		if spec.Secret {
			configured := false
			if text, ok := state.Settings[spec.Key].(string); ok && strings.TrimSpace(text) != "" {
				configured = true
			}
			item["secret_configured"] = configured
			item["editable"] = false
			settings = append(settings, item)
			continue
		}
		item["value"] = effective[spec.Key]
		if value, ok := overrides[spec.Key]; ok {
			item["group_override"] = value
		}
		item["editable"] = pluginSettingEditable(spec, scope, owner) == nil
		settings = append(settings, item)
	}
	return map[string]any{"id": state.Manifest.ID, "name": state.Manifest.Name, "scope": scope, "settings": settings}, nil
}
