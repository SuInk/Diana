package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// bot_config 的 fields 参数按 WebUI 的配置 JSON 键直接改字段：群级是 GroupConfig，
// 机器人级是 ConfigPayload（WebUI 机器人设置页提交的同一份结构）。这样 WebUI 能改的
// 普通设置，聊天里也都能改，新增字段不用再逐个手写。
//
// 拒绝的只有：标识和内部状态、主人与管理员名单及主人权限、Agent 权限、账号安全
// 审核。这几项不放开，是因为聊天内容可能带提示注入，被诱导改掉它们的代价远大于去
// 一趟 WebUI。连接与凭据可以改，经 ConfigFieldsSaver 走 WebUI 的校验和重连。

const fieldsPreviewRunes = 300

var groupFieldRedirects = map[string]string{
	"bot_profile_id":                     "是标识，不能修改",
	"group_id":                           "是标识，不能修改",
	"enabled_set":                        "是内部状态",
	"inheritance_migrated":               "是内部状态",
	"updated_at":                         "是内部状态",
	"whole_mute_restore_permissions":     "是全员禁言的运行时快照",
	"reply_account_safety_audit_enabled": "是账号安全审核设置，只能在 WebUI 修改",
	"reply_account_safety_audit_prompt":  "是账号安全审核设置，只能在 WebUI 修改",
	"chat_model":                         "请用 llm_config 的 scope=group 切换",
	"enabled":                            "请用 group_settings.enabled",
	"group_triggers":                     "请用 group_settings.triggers",
	"group_trigger_mode":                 "请用 group_settings.trigger_mode",
	"plugin_overrides":                   "请用 group_settings.plugins",
	"plugin_setting_overrides":           "请用 plugin_settings",
}

var botFieldRedirects = map[string]string{
	"id":                       "是标识，不能修改",
	"profiles":                 "是机器人列表，请在 WebUI 增删机器人",
	"owner_id":                 "是主人身份，只能在 WebUI 修改",
	"admin_ids":                "是管理员名单，只能在 WebUI 修改",
	"owner_login_enabled":      "是主人登录权限，只能在 WebUI 修改",
	"owner_llm_config_enabled": "是主人改模型的权限，只能在 WebUI 修改",
	"model_roles":              "请用 llm_config 修改模型分配",
}

// botFieldDeniedPrefixes 覆盖 Agent 权限和账号安全审核；botFieldAllowed 是前缀里
// 不涉及权限的例外。
var botFieldDeniedPrefixes = map[string]string{
	"agent_":                      "是 Agent 权限，只能在 WebUI 修改",
	"reply_account_safety_audit_": "是账号安全审核设置，只能在 WebUI 修改",
}

var botFieldAllowed = map[string]bool{"agent_command_timeout_ms": true, "agent_browser_timeout_ms": true}

func botFieldRejection(key string) (string, bool) {
	if reason, ok := botFieldRedirects[key]; ok {
		return reason, true
	}
	if strings.HasSuffix(key, "_configured") || strings.HasSuffix(key, "_preview") {
		return "是只读状态", true
	}
	if botFieldAllowed[key] {
		return "", false
	}
	for prefix, reason := range botFieldDeniedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return reason, true
		}
	}
	return "", false
}

func groupFieldRejection(key string) (string, bool) {
	reason, ok := groupFieldRedirects[key]
	return reason, ok
}

func fieldsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "其余设置 {WebUI配置键:值}，null 清除；先 get 带 list_fields 查键。连接凭据权限不可改。",
		"additionalProperties": map[string]any{},
	}
}

// jsonFieldKeys 列出结构体的全部 JSON 键，用来区分「拼错的键」和「值为空被省略的键」。
func jsonFieldKeys(value any) map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(value)
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

func toJSONMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// patchJSON 把补丁合进 JSON 视图再解回结构体，并确认没被补丁点名的键一个都没变，
// 防止结构体和 JSON 之间的转换悄悄改掉别的设置。
func patchJSON[T any](current T, patch map[string]any, reject func(string) (string, bool)) (T, error) {
	var zero T
	known := jsonFieldKeys(current)
	before, err := toJSONMap(current)
	if err != nil {
		return zero, err
	}
	merged := make(map[string]any, len(before)+len(patch))
	for key, value := range before {
		merged[key] = value
	}
	for key, value := range patch {
		if reason, denied := reject(key); denied {
			return zero, fmt.Errorf("配置项 %q %s", key, reason)
		}
		if !known[key] {
			return zero, fmt.Errorf("没有配置项 %q，先 get 带 list_fields=true 查看可改的键", key)
		}
		if value == nil {
			delete(merged, key)
		} else {
			merged[key] = value
		}
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return zero, err
	}
	var next T
	if err := json.Unmarshal(data, &next); err != nil {
		return zero, fmt.Errorf("配置值类型不对：%w", err)
	}
	return next, nil
}

func unchangedOutsidePatch(before, after map[string]any, patch map[string]any) error {
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		if _, patched := patch[key]; patched {
			continue
		}
		// 凭据的「已配置」「预览」是从凭据派生的只读状态，跟着凭据变是正常的。
		if strings.HasSuffix(key, "_configured") || strings.HasSuffix(key, "_preview") {
			continue
		}
		if !reflect.DeepEqual(before[key], after[key]) {
			return fmt.Errorf("修改会连带改变 %q，已取消；请在 WebUI 修改", key)
		}
	}
	return nil
}

func applyGroupFields(group GroupConfig, patch map[string]any) (GroupConfig, error) {
	next, err := patchJSON(group, patch, groupFieldRejection)
	if err != nil {
		return group, err
	}
	before, _ := toJSONMap(group)
	after, _ := toJSONMap(next)
	if err := unchangedOutsidePatch(before, after, patch); err != nil {
		return group, err
	}
	return NormalizeGroupConfigFields(next)
}

// applyBotFields 走 WebUI 保存机器人设置的同一条转换：ConfigPayload → ConfigFromPayload。
func applyBotFields(cfg BotConfig, patch map[string]any) (BotConfig, error) {
	payload := PayloadFromConfig(cfg)
	nextPayload, err := patchJSON(payload, patch, botFieldRejection)
	if err != nil {
		return cfg, err
	}
	next := ConfigFromPayload(nextPayload, cfg)
	before, _ := toJSONMap(payload)
	after, _ := toJSONMap(PayloadFromConfig(next))
	if err := unchangedOutsidePatch(before, after, patch); err != nil {
		return cfg, err
	}
	if err := next.Validate(); err != nil {
		return cfg, err
	}
	return next, nil
}

func parseFieldsPatch(raw any) (map[string]any, error) {
	patch, ok := raw.(map[string]any)
	if !ok || len(patch) == 0 {
		return nil, fmt.Errorf("fields 必须是非空对象，键为配置项")
	}
	return patch, nil
}

func fieldKeys(patch map[string]any) []string {
	keys := make([]string, 0, len(patch))
	for key := range patch {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// editableFieldsView 列出可在聊天里改的键和当前值，长文本截断，凭据类键已被拒绝规则排除。
func editableFieldsView(value any, reject func(string) (string, bool)) map[string]any {
	current, err := toJSONMap(value)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	for key := range jsonFieldKeys(value) {
		if _, denied := reject(key); denied {
			continue
		}
		item := current[key]
		if text, ok := item.(string); ok && utf8.RuneCountInString(text) > fieldsPreviewRunes {
			item = string([]rune(text)[:fieldsPreviewRunes]) + fmt.Sprintf("…（共 %d 字，已截断）", utf8.RuneCountInString(text))
		}
		out[key] = item
	}
	return out
}

// ConfigFieldsSaver 由 WebUI 实现，让 fields 走 WebUI 保存设置的完整链路：连接冲突、
// 令牌强度、搜索覆盖、扩展权限上限等校验，以及连接参数变化后的重连。未接入时
// 退回运行时自己的保存，只适合不涉及连接的字段。
type ConfigFieldsSaver interface {
	SaveBotFields(ctx context.Context, profileID string, patch func(BotConfig) (BotConfig, error)) (BotConfig, error)
	SaveGroupFields(ctx context.Context, cfg GroupConfig, previous GroupConfig, base BotConfig, byOwner bool) (GroupConfig, error)
}

// SetConfigFieldsSaver 接入 WebUI 的设置保存链路。
func (r *Runtime) SetConfigFieldsSaver(saver ConfigFieldsSaver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configFieldsSaver = saver
}

func (r *Runtime) currentConfigFieldsSaver() ConfigFieldsSaver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.configFieldsSaver
}
