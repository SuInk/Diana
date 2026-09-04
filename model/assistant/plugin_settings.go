// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"math"
	"strings"
)

// 插件设置项类型，WebUI 按类型渲染对应的表单控件。
const (
	PluginSettingTypeBool               = "bool"
	PluginSettingTypeNumber             = "number"
	PluginSettingTypeString             = "string"
	PluginSettingTypeSelect             = "select"
	PluginSettingTypeMultiSelect        = "multi_select"
	PluginSettingTypePlatformLevelRules = "platform_level_rules"
	// PluginSettingTypeRelayEndpoints 配置一组参与双向消息互通的会话端点。
	PluginSettingTypeRelayEndpoints = "relay_endpoints"
	// PluginSettingTypeText 渲染为多行文本框，用于模板这类带换行的配置。
	PluginSettingTypeText = "text"
	// PluginSettingTypeSize 的值统一是字节数，WebUI 渲染成「数字 + 单位」，
	// 避免用户在纯数字输入框里按错数量级。
	PluginSettingTypeSize = "size"
)

type PluginSettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type PluginSettingSpec struct {
	Key         string                `json:"key"`
	Label       string                `json:"label"`
	Description string                `json:"description,omitempty"`
	Type        string                `json:"type"`
	Default     any                   `json:"default"`
	Min         *float64              `json:"min,omitempty"`
	Max         *float64              `json:"max,omitempty"`
	Step        float64               `json:"step,omitempty"`
	Unit        string                `json:"unit,omitempty"`
	Options     []PluginSettingOption `json:"options,omitempty"`
	// Rows 是多行文本框的建议行高。模板类设置默认的四行装不下带
	// <dianabr> 的多行模板，编辑时全靠滚动，给它一个更合身的高度。
	Rows int `json:"rows,omitempty"`
	// Secret 标记凭据类设置（Cookie、密钥等）。这类值读接口一律不回传明文，
	// 前端用密码框 + 「已配置」徽章展示，提交空串表示保持原值不变。
	Secret bool `json:"secret,omitempty"`
}

// secretSettingKeys 返回声明为凭据的设置键。
func secretSettingKeys(specs []PluginSettingSpec) map[string]bool {
	out := map[string]bool{}
	for _, spec := range specs {
		if spec.Secret {
			out[spec.Key] = true
		}
	}
	return out
}

// SettingValues 是运行时注入插件请求的生效设置，读取方法在键缺失或类型不符时返回兜底值。
type SettingValues map[string]any

// PluginSettingOverrides stores explicit non-secret setting overrides by
// plugin ID for one conversation scope. Missing plugins and keys inherit the
// global plugin state.
type PluginSettingOverrides map[string]map[string]any

// Bool 读取布尔设置。
func (v SettingValues) Bool(key string, fallback bool) bool {
	if value, ok := v[key].(bool); ok {
		return value
	}
	return fallback
}

// Int 读取整数设置，JSON 解码出的 float64 会四舍五入。
func (v SettingValues) Int(key string, fallback int) int {
	number, ok := numberValue(v[key])
	if !ok {
		return fallback
	}
	return int(math.Round(number))
}

// Bytes 读取字节数设置（PluginSettingTypeSize），JSON 解码出的 float64 会四舍五入。
func (v SettingValues) Bytes(key string, fallback int64) int64 {
	number, ok := numberValue(v[key])
	if !ok {
		return fallback
	}
	return int64(math.Round(number))
}

// String 读取字符串设置，空白值视为未设置。
func (v SettingValues) String(key string, fallback string) string {
	if value, ok := v[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

// StringSlice 读取字符串数组设置，兼容 JSON 解码出的 []any。
func (v SettingValues) StringSlice(key string) []string {
	switch value := v[key].(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

// normalizePluginSettings 严格校验提交的设置值，未知键或类型错误都会拒绝。
func normalizePluginSettings(specs []PluginSettingSpec, values map[string]any) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	byKey := make(map[string]PluginSettingSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}
	out := make(map[string]any, len(values))
	for key, raw := range values {
		spec, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("diana: unknown plugin setting %q", key)
		}
		value, err := normalizeSettingValue(spec, raw)
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

// sanitizePluginSettings 尽力恢复持久化的设置值，非法条目直接丢弃而不是整体失败。
func sanitizePluginSettings(specs []PluginSettingSpec, values map[string]any) map[string]any {
	if len(values) == 0 || len(specs) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, spec := range specs {
		raw, ok := values[spec.Key]
		if !ok {
			continue
		}
		value, err := normalizeSettingValue(spec, raw)
		if err != nil {
			continue
		}
		out[spec.Key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeGroupPluginSettings validates one plugin's group-level overrides.
// Credentials stay global so group configuration responses can never expose
// plugin secrets.
func normalizeGroupPluginSettings(specs []PluginSettingSpec, values map[string]any) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	secrets := secretSettingKeys(specs)
	for key := range values {
		if secrets[key] {
			return nil, fmt.Errorf("diana: secret plugin setting %q cannot be overridden per group", key)
		}
	}
	return normalizePluginSettings(nonSecretPluginSettingSpecs(specs), values)
}

// sanitizeGroupPluginSettings tolerates stale persisted data while excluding
// unknown and secret fields from both runtime use and API responses.
func sanitizeGroupPluginSettings(specs []PluginSettingSpec, values map[string]any) map[string]any {
	return sanitizePluginSettings(nonSecretPluginSettingSpecs(specs), values)
}

func nonSecretPluginSettingSpecs(specs []PluginSettingSpec) []PluginSettingSpec {
	out := make([]PluginSettingSpec, 0, len(specs))
	for _, spec := range specs {
		if !spec.Secret {
			out = append(out, spec)
		}
	}
	return out
}

// normalizeSettingValue 按设置项类型校验并收敛单个值，数字会被夹到 Min/Max 范围内。
func normalizeSettingValue(spec PluginSettingSpec, raw any) (any, error) {
	switch spec.Type {
	case PluginSettingTypeBool:
		value, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("diana: setting %q expects a boolean", spec.Key)
		}
		return value, nil
	case PluginSettingTypeNumber:
		number, ok := numberValue(raw)
		if !ok {
			return nil, fmt.Errorf("diana: setting %q expects a number", spec.Key)
		}
		if spec.Min != nil && number < *spec.Min {
			number = *spec.Min
		}
		if spec.Max != nil && number > *spec.Max {
			number = *spec.Max
		}
		return number, nil
	case PluginSettingTypeSize:
		number, ok := numberValue(raw)
		if !ok {
			return nil, fmt.Errorf("qqbot: setting %q expects a size in bytes", spec.Key)
		}
		if spec.Min != nil && number < *spec.Min {
			number = *spec.Min
		}
		if spec.Max != nil && number > *spec.Max {
			number = *spec.Max
		}
		// 字节数没有小数概念，落库前取整。
		return math.Round(number), nil
	case PluginSettingTypeString:
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("diana: setting %q expects a string", spec.Key)
		}
		return strings.TrimSpace(value), nil
	case PluginSettingTypeSelect:
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("diana: setting %q expects a string option", spec.Key)
		}
		for _, option := range spec.Options {
			if option.Value == value {
				return value, nil
			}
		}
		return nil, fmt.Errorf("diana: setting %q got unsupported option %q", spec.Key, value)
	case PluginSettingTypeMultiSelect:
		items, err := stringSliceValue(raw)
		if err != nil {
			return nil, fmt.Errorf("diana: setting %q expects a string array", spec.Key)
		}
		allowed := make(map[string]bool, len(spec.Options))
		for _, option := range spec.Options {
			allowed[option.Value] = true
		}
		out := make([]string, 0, len(items))
		seen := map[string]bool{}
		for _, item := range items {
			if !allowed[item] {
				return nil, fmt.Errorf("diana: setting %q got unsupported option %q", spec.Key, item)
			}
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
		return out, nil
	case PluginSettingTypePlatformLevelRules:
		return normalizePlatformLevelRules(spec, raw)
	case PluginSettingTypeRelayEndpoints:
		return normalizeRelayEndpoints(spec, raw)
	default:
		return nil, fmt.Errorf("diana: setting %q has unsupported type %q", spec.Key, spec.Type)
	}
}

func normalizeRelayEndpoints(spec PluginSettingSpec, raw any) ([]map[string]any, error) {
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]map[string]any); typedOK {
			items = make([]any, len(typed))
			for i := range typed {
				items[i] = typed[i]
			}
		} else {
			return nil, fmt.Errorf("diana: setting %q expects an endpoint array", spec.Key)
		}
	}
	out := make([]map[string]any, 0, len(items))
	seen := map[string]bool{}
	for index, item := range items {
		endpoint, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("diana: setting %q endpoint %d must be an object", spec.Key, index+1)
		}
		profileID, _ := endpoint["profile_id"].(string)
		platform, _ := endpoint["platform"].(string)
		kind, _ := endpoint["kind"].(string)
		targetID, _ := endpoint["target_id"].(string)
		profileID = strings.TrimSpace(profileID)
		platform = NormalizePlatformID(platform)
		kind = strings.TrimSpace(strings.ToLower(kind))
		targetID = strings.TrimSpace(targetID)
		if profileID == "" || platform == "" || targetID == "" {
			return nil, fmt.Errorf("diana: setting %q endpoint %d is incomplete", spec.Key, index+1)
		}
		if _, ok := PlatformByID(platform); !ok {
			return nil, fmt.Errorf("diana: setting %q endpoint %d has unsupported platform %q", spec.Key, index+1, platform)
		}
		if kind != "group" && kind != "private" {
			return nil, fmt.Errorf("diana: setting %q endpoint %d has invalid kind", spec.Key, index+1)
		}
		key := strings.Join([]string{profileID, platform, kind, targetID}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, map[string]any{"profile_id": profileID, "platform": platform, "kind": kind, "target_id": targetID})
	}
	return out, nil
}

func normalizePlatformLevelRules(spec PluginSettingSpec, raw any) ([]map[string]any, error) {
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]map[string]any); typedOK {
			items = make([]any, len(typed))
			for index := range typed {
				items[index] = typed[index]
			}
		} else {
			return nil, fmt.Errorf("diana: setting %q expects a rule array", spec.Key)
		}
	}
	allowed := map[string]bool{}
	for _, option := range spec.Options {
		allowed[option.Value] = true
	}
	out := make([]map[string]any, 0, len(items))
	for index, item := range items {
		rule, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("diana: setting %q rule %d must be an object", spec.Key, index+1)
		}
		platform, _ := rule["platform"].(string)
		platform = strings.TrimSpace(strings.ToLower(platform))
		if !allowed[platform] {
			return nil, fmt.Errorf("diana: setting %q rule %d has unsupported platform %q", spec.Key, index+1, platform)
		}
		minimum, ok := numberValue(rule["minimum_level"])
		if !ok || minimum < 0 || minimum > maximumReplyMemberLevel {
			return nil, fmt.Errorf("diana: setting %q rule %d minimum_level must be 0 to %d", spec.Key, index+1, maximumReplyMemberLevel)
		}
		unknown, _ := rule["unknown_policy"].(string)
		unknown = strings.TrimSpace(strings.ToLower(unknown))
		if unknown != LevelUnknownAllow && unknown != LevelUnknownDeny {
			return nil, fmt.Errorf("diana: setting %q rule %d has invalid unknown_policy", spec.Key, index+1)
		}
		out = append(out, map[string]any{
			"platform":       platform,
			"minimum_level":  math.Round(minimum),
			"unknown_policy": unknown,
			"owner_bypass":   boolValueFromMap(rule, "owner_bypass", true),
			"mention_bypass": boolValueFromMap(rule, "mention_bypass", false),
			"enabled":        boolValueFromMap(rule, "enabled", true),
		})
	}
	return out, nil
}

func boolValueFromMap(values map[string]any, key string, fallback bool) bool {
	if value, ok := values[key].(bool); ok {
		return value
	}
	return fallback
}

// effectivePluginSettings 用声明的默认值合并用户覆盖值，作为插件运行时读到的生效设置。
func effectivePluginSettings(specs []PluginSettingSpec, overrides map[string]any) SettingValues {
	if len(specs) == 0 {
		return nil
	}
	out := make(SettingValues, len(specs))
	for _, spec := range specs {
		out[spec.Key] = spec.Default
		if value, ok := overrides[spec.Key]; ok {
			out[spec.Key] = value
		}
	}
	return out
}

// effectivePluginSettingsForGroup applies group overrides after global
// overrides. Group values are sanitized defensively because old persisted
// configurations may predate the current manifest.
func effectivePluginSettingsForGroup(specs []PluginSettingSpec, global, group map[string]any) SettingValues {
	out := effectivePluginSettings(specs, global)
	for key, value := range sanitizeGroupPluginSettings(specs, group) {
		out[key] = value
	}
	return out
}

// stringSliceValue 兼容 JSON 解码和 Go 侧直接赋值的字符串数组。
func stringSliceValue(raw any) ([]string, error) {
	switch value := raw.(type) {
	case []string:
		return value, nil
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("not a string array")
			}
			out = append(out, text)
		}
		return out, nil
	}
	return nil, fmt.Errorf("not a string array")
}

// numberValue 兼容 JSON 解码和 Go 侧直接赋值的数字类型。
func numberValue(raw any) (float64, bool) {
	switch number := raw.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	}
	return 0, false
}

// settingRange 构造设置范围用的 float64 指针。
func settingRange(value float64) *float64 {
	return &value
}
