package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
)

const consoleConfigToolName = "console_config"

// ConsoleConfigManager exposes named, validated settings operations, never an
// arbitrary URL, method, SQL statement or filesystem path supplied by a model.
type ConsoleConfigManager interface {
	Catalog() any
	Read(context.Context, string, string, string) (any, error)
	Apply(context.Context, string, string, string, string, map[string]any) (any, error)
}

type pendingConsoleConfig struct {
	resource, action, target, messageID string
	payload                             map[string]any
	expires                             time.Time
}

func (r *Runtime) SetConsoleConfigManager(manager ConsoleConfigManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consoleConfig = manager
}
func (r *Runtime) consoleConfigManager() ConsoleConfigManager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.consoleConfig
}

type dianaConsoleConfigTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *dianaConsoleConfigTool) Name() string { return consoleConfigToolName }
func (t *dianaConsoleConfigTool) Description() string {
	return "管理控制台配置：机器人全部可编辑字段（含各用途模型完整路由、思考强度、权重、后备、排序、分组和参数）、提供商、插件凭据、缓存、媒体保留、访问安全、登录会话和受信应用。先 catalog/get 查询。stage 暂存改动并返回脱敏预览，不生效；用户在同一会话下一条回复确认后才 confirm，无需确认码。每次只确认最后一份预览；密钥和 URL 不得回显。"
}
func (t *dianaConsoleConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("查询或暂存/确认/放弃修改", "catalog", "get", "stage", "confirm", "discard"),
		"resource":  toolStringParam("catalog 返回的资源名"),
		"action":    toolStringParam("catalog 返回的操作名"),
		"target":    toolStringParam("资源标识；机器人默认当前机器人；插件用插件 ID"),
		"payload":   map[string]any{"type": "object", "additionalProperties": map[string]any{}, "description": "改动的字段；更新为补丁，数组按完整列表替换；不可把脱敏占位符作为新值"},
	})
}
func (t *dianaConsoleConfigTool) CanonicalOperation(input map[string]any) string {
	return configToolString(input, "operation")
}
func (t *dianaConsoleConfigTool) Run(ctx context.Context, input map[string]any) (out string, err error) {
	defer func() {
		out = secretmask.ChatConfigJSON(out)
		if err != nil {
			err = secretmask.ChatConfigError(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r := t.runtime
	cfg := r.effectiveConfigForEvent(t.event)
	admin := isAdminChatRun(ctx)
	if !admin && (t.event.Kind != EventKindPrivate || !cfg.IsOwnerEvent(t.event)) {
		return "", fmt.Errorf("仅主人私聊或控制台管理对话可以管理这些设置")
	}
	manager := r.consoleConfigManager()
	if manager == nil {
		return "", fmt.Errorf("当前部署未接入控制台配置管理")
	}
	op := t.CanonicalOperation(input)
	if cfg.agentSafeMode() && op != "catalog" && op != "get" && op != "discard" {
		return "", fmt.Errorf("%s", agentSafeModeDisabledMessage)
	}
	session := configPendingKey(t.event)
	if admin && t.event.ContextNamespace == "" {
		return "", fmt.Errorf("管理对话缺少会话标识，请刷新页面")
	}
	reply := func(v any) (string, error) { b, e := json.Marshal(secretmask.ChatConfigValue(v)); return string(b), e }
	resource, target := configToolString(input, "resource"), configToolString(input, "target")
	switch op {
	case "catalog":
		return reply(manager.Catalog())
	case "get":
		value, e := manager.Read(ctx, cfg.ID, resource, target)
		if e != nil {
			return "", e
		}
		return reply(value)
	case "stage":
		catalog, ok := manager.Catalog().(map[string]any)
		resourceInfo, exists := catalog[resource].(map[string]any)
		if !ok || !exists {
			return "", fmt.Errorf("未知资源，请先 catalog")
		}
		actions, _ := resourceInfo["actions"].([]string)
		valid := false
		for _, action := range actions {
			if action == configToolString(input, "action") {
				valid = true
			}
		}
		if !valid {
			return "", fmt.Errorf("不支持的操作，请先 catalog")
		}
		payload, ok := input["payload"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("payload 必须是对象")
		}
		if t.event.MessageID == "" {
			return "", fmt.Errorf("缺少消息标识，不能暂存改动")
		}
		// Detach from mutable model arguments. Validation and ownership are rechecked
		// by the manager on commit against current persisted settings.
		secretmask.RegisterChatConfigSecrets(payload)
		b, e := json.Marshal(payload)
		if e != nil {
			return "", e
		}
		var copy map[string]any
		if e = json.Unmarshal(b, &copy); e != nil {
			return "", e
		}
		r.mu.Lock()
		if r.pendingConsoleConfig == nil {
			r.pendingConsoleConfig = map[string]pendingConsoleConfig{}
		}
		for k, p := range r.pendingConsoleConfig {
			if time.Now().After(p.expires) {
				delete(r.pendingConsoleConfig, k)
			}
		}
		delete(r.pendingBotFields, session)
		r.pendingConsoleConfig[session] = pendingConsoleConfig{resource: resource, action: configToolString(input, "action"), target: target, payload: copy, messageID: t.event.MessageID, expires: time.Now().Add(pendingBotFieldsTTL)}
		r.mu.Unlock()
		preview := payload
		if resource == "plugin" {
			preview = map[string]any{}
			for key, value := range payload {
				preview[key] = value
			}
			if settings, ok := payload["settings"].(map[string]any); ok {
				hidden := map[string]any{}
				var secrets map[string]bool
				known := false
				if r.plugins != nil {
					if state, ok := r.plugins.Get(target); ok {
						known = true
						secrets = secretSettingKeys(state.Manifest.Settings)
					}
				}
				for key, value := range settings {
					if !known || secrets[key] {
						hidden[key] = "[REDACTED]"
						if text, ok := value.(string); ok {
							secretmask.Register(text)
						}
					} else {
						hidden[key] = value
					}
				}
				preview["settings"] = hidden
			}
		}
		return reply(map[string]any{"pending": true, "resource": resource, "action": configToolString(input, "action"), "target": target, "changes": preview, "message": "以上改动尚未生效。请在下一条消息回复确认，无需确认码；10 分钟内有效，新的暂存替换旧的。"})
	case "discard":
		r.mu.Lock()
		delete(r.pendingConsoleConfig, session)
		r.mu.Unlock()
		return reply(map[string]any{"discarded": true})
	case "confirm":
		if !isConfigConfirmation(hostCommandRequestFromContext(ctx, t.event)) {
			return "", fmt.Errorf("请在下一条消息回复确认，无需确认码")
		}
		r.mu.Lock()
		pending, ok := r.pendingConsoleConfig[session]
		if !ok || time.Now().After(pending.expires) {
			delete(r.pendingConsoleConfig, session)
			r.mu.Unlock()
			return "", fmt.Errorf("没有待确认的改动或已过期")
		}
		if t.event.MessageID == "" || t.event.MessageID == pending.messageID {
			r.mu.Unlock()
			return "", fmt.Errorf("请在下一条用户消息中确认")
		}
		delete(r.pendingConsoleConfig, session)
		r.mu.Unlock()
		_, hasModelRoles := pending.payload["model_roles"]
		_, hasGroupModel := pending.payload["chat_model"]
		if !admin && !boolValue(cfg.OwnerLLMConfigEnabled, true) && (pending.resource == "provider" || pending.resource == "bot" && hasModelRoles || pending.resource == "group" && hasGroupModel) {
			return "", fmt.Errorf("聊天内模型管理已关闭；请先明确启用该权限，再暂存模型改动")
		}
		value, e := manager.Apply(ctx, cfg.ID, pending.resource, pending.action, pending.target, pending.payload)
		if e != nil {
			return "", e
		}
		return reply(value)
	default:
		return "", fmt.Errorf("未知操作，请先 catalog")
	}
}
