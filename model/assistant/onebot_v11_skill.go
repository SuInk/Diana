package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	onebotv11skill "github.com/SuInk/diana/skills/onebot-v11"
)

const (
	oneBotV11PluginID      = "official.onebot-v11-skill"
	dianaOneBotV11ToolName = "diana.onebot_v11"
	oneBotV11SkillSource   = "builtin:official.onebot-v11-skill"
)

var (
	oneBotV11ActionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	oneBotV11MemberReadActions = map[string]bool{
		"get_msg":               true,
		"get_forward_msg":       true,
		"get_login_info":        true,
		"get_stranger_info":     true,
		"get_friend_list":       true,
		"get_group_info":        true,
		"get_group_list":        true,
		"get_group_member_info": true,
		"get_group_member_list": true,
		"get_group_honor_info":  true,
		"get_record":            true,
		"get_image":             true,
		"can_send_image":        true,
		"can_send_record":       true,
		"get_status":            true,
		"get_version_info":      true,
	}
	oneBotV11SensitiveReadActions = map[string]bool{
		"get_cookies":     true,
		"get_csrf_token":  true,
		"get_credentials": true,
	}
)

// OneBotV11SkillPlugin exposes the capability in Diana's built-in plugin
// catalog. Natural-language routing stays with the main Agent.
type OneBotV11SkillPlugin struct{}

func NewOneBotV11SkillPlugin() *OneBotV11SkillPlugin {
	return &OneBotV11SkillPlugin{}
}

func (p *OneBotV11SkillPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          oneBotV11PluginID,
		Name:        "OneBot v11 协议技能",
		Version:     "0.1.0",
		Description: "官方内置 OneBot v11 结构化调用能力；主人可调用全部标准及实现扩展动作，普通成员仅可调用明确的标准只读动作。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"onebot:read", "onebot:write:owner", "onebot:credentials:owner"},
	}
}

func (p *OneBotV11SkillPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type dianaOneBotV11Tool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaOneBotV11Tool(runtime *Runtime, event MessageEvent) *dianaOneBotV11Tool {
	return &dianaOneBotV11Tool{runtime: runtime, event: event}
}

func (t *dianaOneBotV11Tool) Name() string {
	return dianaOneBotV11ToolName
}

func (t *dianaOneBotV11Tool) Description() string {
	return `调用当前 QQ 连接的 OneBot v11 action。仅在用户明确要求读取 OneBot/QQ 信息或执行 QQ 操作时调用。主人可调用全部标准动作及当前实现提供的扩展；普通成员仅可调用后端固定的标准只读白名单，凭据读取、修改动作和未知扩展一律拒绝。input: {"action":"OneBot action","params":{"协议原始参数":"值"}}`
}

func (t *dianaOneBotV11Tool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("OneBot v11: runtime is not configured")
	}
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	access := "member_read_only"
	if owner {
		access = "owner_full"
	}
	action, actionErr := oneBotV11ActionFromInput(input)
	if actionErr != nil {
		t.runtime.recordOneBotV11Action(t.event, "[invalid]", access, owner, nil, actionErr)
		return "", actionErr
	}
	params, paramsErr := oneBotV11ParamsFromInput(input)
	if paramsErr != nil {
		t.runtime.recordOneBotV11Action(t.event, action, access, owner, nil, paramsErr)
		return "", paramsErr
	}
	if !t.runtime.oneBotV11SkillEnabled(t.event) {
		err := fmt.Errorf("OneBot v11 协议技能未启用，或当前消息不来自 OneBot 平台")
		t.runtime.recordOneBotV11Action(t.event, action, access, owner, sortedMapKeys(params), err)
		return "", err
	}
	if !owner && !oneBotV11MemberActionAllowed(action) {
		err := fmt.Errorf("普通成员仅可读取 OneBot v11 信息，action %q 需要主人权限", action)
		t.runtime.recordOneBotV11Action(t.event, action, access, false, sortedMapKeys(params), err)
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := t.runtime.callOneBotAPIForEvent(callCtx, t.event, action, params)
	if err != nil {
		wrapped := fmt.Errorf("OneBot v11 action %q failed: %w", action, err)
		t.runtime.recordOneBotV11Action(t.event, action, access, owner, sortedMapKeys(params), wrapped)
		return "", wrapped
	}
	t.runtime.recordOneBotV11Action(t.event, action, access, owner, sortedMapKeys(params), nil)
	body, err := json.Marshal(map[string]any{
		"ok":     true,
		"action": action,
		"access": access,
		"data":   data,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func oneBotV11ActionFromInput(input map[string]any) (string, error) {
	raw, ok := input["action"].(string)
	if !ok {
		return "", fmt.Errorf("OneBot v11 action must be a string")
	}
	action := strings.TrimSpace(raw)
	if !oneBotV11ActionNamePattern.MatchString(action) {
		return "", fmt.Errorf("invalid OneBot v11 action name")
	}
	return action, nil
}

func oneBotV11ParamsFromInput(input map[string]any) (map[string]any, error) {
	raw, ok := input["params"]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	switch value := raw.(type) {
	case map[string]any:
		return value, nil
	case string:
		var params map[string]any
		if err := json.Unmarshal([]byte(value), &params); err != nil || params == nil {
			return nil, fmt.Errorf("OneBot v11 params must be a JSON object")
		}
		return params, nil
	default:
		return nil, fmt.Errorf("OneBot v11 params must be an object")
	}
}

func oneBotV11MemberActionAllowed(action string) bool {
	return oneBotV11MemberReadActions[oneBotV11BaseAction(action)]
}

func oneBotV11BaseAction(action string) string {
	base := strings.TrimSpace(action)
	for _, suffix := range []string{"_async", "_rate_limited"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return base
}

func oneBotV11SensitiveAction(action string) bool {
	return oneBotV11SensitiveReadActions[oneBotV11BaseAction(action)]
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r *Runtime) oneBotV11SkillEnabled(event MessageEvent) bool {
	if r == nil || r.plugins == nil {
		return false
	}
	if !r.plugins.EnabledWithOverrides(oneBotV11PluginID, r.pluginOverridesForEvent(event)) {
		return false
	}
	platform := strings.TrimSpace(event.Platform)
	if platform == "" {
		platform = r.effectiveConfigForEvent(event).Platform
	}
	return IsOneBotPlatform(platform)
}

func (r *Runtime) oneBotV11BuiltinSkills(event MessageEvent) []agent.SkillMetadata {
	if !r.oneBotV11SkillEnabled(event) {
		return nil
	}
	return []agent.SkillMetadata{{
		Name:             "onebot-v11",
		Description:      "Safely inspect or operate the current QQ bot through OneBot v11 with owner-full and member-read-only authorization.",
		ShortDescription: "安全调用 OneBot v11 与实现扩展动作",
		Path:             "builtin://onebot-v11/SKILL.md",
		Source:           oneBotV11SkillSource,
		Content:          onebotv11skill.Markdown(),
	}}
}

func (r *Runtime) recordOneBotV11Action(event MessageEvent, action, access string, owner bool, paramKeys []string, callErr error) {
	if r == nil {
		return
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	kind := applog.KindOperation
	level := applog.LevelInfo
	message := "OneBot v11 action 调用成功"
	detail := ""
	if callErr != nil {
		kind = applog.KindError
		level = applog.LevelError
		message = "OneBot v11 action 调用被拒绝或失败"
		detail = "OneBot v11 action failed or was denied"
		if oneBotV11SensitiveAction(action) {
			detail = "sensitive OneBot v11 action failed or was denied"
		}
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  "qqbot.onebot_v11.action",
		Message: message,
		Detail:  detail,
		Actor:   qqEventActor(event),
		Target:  action,
		Metadata: map[string]any{
			"action":     action,
			"access":     access,
			"owner":      owner,
			"sensitive":  oneBotV11SensitiveAction(action),
			"param_keys": append([]string(nil), paramKeys...),
			"platform":   event.Platform,
			"profile_id": event.ProfileID,
			"group_id":   event.GroupID,
			"user_id":    event.UserID,
			"message_id": event.MessageID,
		},
	})
}
