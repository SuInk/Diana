package assistant

import (
	"fmt"
	"math"
	"strings"
)

// 插件设置项类型，WebUI 按类型渲染对应的表单控件。
const (
	PluginSettingTypeBool        = "bool"
	PluginSettingTypeNumber      = "number"
	PluginSettingTypeString      = "string"
	PluginSettingTypeSelect      = "select"
	PluginSettingTypeMultiSelect = "multi_select"
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
			return nil, fmt.Errorf("qqbot: unknown plugin setting %q", key)
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
			return nil, fmt.Errorf("qqbot: secret plugin setting %q cannot be overridden per group", key)
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
			return nil, fmt.Errorf("qqbot: setting %q expects a boolean", spec.Key)
		}
		return value, nil
	case PluginSettingTypeNumber:
		number, ok := numberValue(raw)
		if !ok {
			return nil, fmt.Errorf("qqbot: setting %q expects a number", spec.Key)
		}
		if spec.Min != nil && number < *spec.Min {
			number = *spec.Min
		}
		if spec.Max != nil && number > *spec.Max {
			number = *spec.Max
		}
		return number, nil
	case PluginSettingTypeString:
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("qqbot: setting %q expects a string", spec.Key)
		}
		return strings.TrimSpace(value), nil
	case PluginSettingTypeSelect:
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("qqbot: setting %q expects a string option", spec.Key)
		}
		for _, option := range spec.Options {
			if option.Value == value {
				return value, nil
			}
		}
		return nil, fmt.Errorf("qqbot: setting %q got unsupported option %q", spec.Key, value)
	case PluginSettingTypeMultiSelect:
		items, err := stringSliceValue(raw)
		if err != nil {
			return nil, fmt.Errorf("qqbot: setting %q expects a string array", spec.Key)
		}
		allowed := make(map[string]bool, len(spec.Options))
		for _, option := range spec.Options {
			allowed[option.Value] = true
		}
		out := make([]string, 0, len(items))
		seen := map[string]bool{}
		for _, item := range items {
			if !allowed[item] {
				return nil, fmt.Errorf("qqbot: setting %q got unsupported option %q", spec.Key, item)
			}
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("qqbot: setting %q has unsupported type %q", spec.Key, spec.Type)
	}
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
