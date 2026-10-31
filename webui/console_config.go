package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type consoleConfigContextKey struct{}

// ConsoleConfigManager binds a fixed set of settings to the same handlers used
// by the authenticated console. Callers cannot supply routes or HTTP methods.
type ConsoleConfigManager struct {
	mu      sync.Mutex
	bot     *BotHandler
	llm     *LLMConfigHandler
	cache   *MediaCacheHandler
	media   *HistoryMediaHandler
	auth    *AuthHandler
	trusted *TrustedLoginHandler
}

func NewConsoleConfigManager(bot *BotHandler, llm *LLMConfigHandler, cache *MediaCacheHandler, media *HistoryMediaHandler, auth *AuthHandler, trusted *TrustedLoginHandler) *ConsoleConfigManager {
	return &ConsoleConfigManager{bot: bot, llm: llm, cache: cache, media: media, auth: auth, trusted: trusted}
}
func (m *ConsoleConfigManager) Catalog() any {
	return map[string]any{
		"bot":               map[string]any{"actions": []string{"update", "create", "clone", "delete"}, "fields": consoleFieldTypes(assistant.ConfigPayload{}), "hint": "target 默认当前机器人；update 是顶层补丁；model_roles 按用途整体替换，可设所有用途、各路由 reasoning_effort/weight/standby/disabled/group/fallbacks/routing_strategy/params。删除用途键恢复继承。身份、权限与连接修改确认后生效。至少保留一个机器人。"},
		"provider":          map[string]any{"actions": []string{"update", "create", "clone", "delete", "reorder"}, "fields": consoleFieldTypes(llmConfigPayload{}), "hint": "target 使用 get 返回的提供商 ID；update 只提交变化字段；models 为完整目录；group 为提供商分组；凭据和地址不回显；reorder 提交 ids 顺序。"},
		"plugin":            map[string]any{"actions": []string{"update"}, "hint": "target 为插件 ID，payload.settings 为完整非敏感覆盖加本次修改的密钥；省略密钥保留原值，clear_secrets 指定清除。get 返回插件 schema。"},
		"media_cache":       map[string]any{"actions": []string{"update"}, "fields": []string{"retention_days", "max_mb"}},
		"history_media":     map[string]any{"actions": []string{"update"}, "fields": []string{"retention_days", "max_mb"}, "hint": "修改后会按新策略清理历史媒体。"},
		"security":          map[string]any{"actions": []string{"update"}, "fields": []string{"new_username", "new_password"}, "hint": "更新控制台凭据会注销已有会话。"},
		"sessions":          map[string]any{"actions": []string{"delete", "delete_all"}, "hint": "target 为会话 ID；delete_all 注销全部控制台会话，包括当前会话。"},
		"trusted_apps":      map[string]any{"actions": []string{"enable", "create", "update", "rotate"}, "fields": []string{"id", "name", "profile_id", "disabled", "enabled", "secret"}, "hint": "enable 修改总开关；create/rotate 必须提供至少 32 字符的 secret，由用户保存，聊天不回显；update 使用完整 name/profile_id/disabled。"},
		"group":             map[string]any{"actions": []string{"update"}, "fields": consoleFieldTypes(assistant.GroupConfig{}), "hint": "target 为当前机器人的群 ID；chat_model.route 设置独立完整对话路由（disabled/weight/standby/reasoning_effort/fallbacks/routing_strategy）；chat_model={} 恢复继承。"},
		"model_role_fields": consoleFieldTypes(assistant.ModelRole{}),
		"readonly":          []string{"标识和派生状态不能修改；浏览器主题仅存浏览器本地，不是机器人配置"},
	}
}
func consoleFieldTypes(value any) map[string]string {
	out := map[string]string{}
	typ := reflect.TypeOf(value)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		key := strings.Split(f.Tag.Get("json"), ",")[0]
		if key != "" && key != "-" && !consoleReadonlyField(key) {
			out[key] = f.Type.String()
		}
	}
	return out
}
func consoleReadonlyField(key string) bool {
	return strings.HasPrefix(key, "effective_") || key == "role_bindings" || key == "context_window_source" || key == "catalog_context_window_tokens" || key == "max_output_tokens_source" || key == "default_user_agent" || key == "bot_profile_id" || key == "group_id" || key == "enabled_set" || key == "inheritance_migrated" || key == "whole_mute_restore_permissions" || key == "id" || key == "profiles" || key == "updated_at" || strings.HasSuffix(key, "_configured") || strings.HasSuffix(key, "_preview")
}
func consoleMap(value any) map[string]any {
	b, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}
func consoleMerge(current any, patch map[string]any) (map[string]any, error) {
	merged := consoleMap(current)
	known := consoleFieldTypes(current)
	for key, value := range patch {
		if _, ok := known[key]; !ok {
			return nil, fmt.Errorf("配置项 %s 不存在或只读", key)
		}
		if text, ok := value.(string); ok && (strings.Contains(text, "[REDACTED]") || strings.Contains(text, "[URL REDACTED]")) {
			return nil, fmt.Errorf("不能把脱敏占位符保存为配置")
		}
		merged[key] = value
	}
	return merged, nil
}
func (m *ConsoleConfigManager) Read(ctx context.Context, profile, resource, target string) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch resource {
	case "bot":
		if target != "" && target != profile {
			return nil, fmt.Errorf("请先切换到目标机器人的管理对话")
		}
		for _, cfg := range m.bot.profiles.Profiles().Profiles {
			if cfg.ID == profile {
				return secretmask.ChatConfigValue(assistant.PayloadFromConfig(cfg)), nil
			}
		}
	case "group":
		if target == "" {
			return nil, fmt.Errorf("请指定群 ID")
		}
		cfg, ok := m.bot.groupConfigs.ConfigForGroup(profile, target)
		if !ok {
			cfg = assistant.DefaultGroupConfig(target, m.bot.runtime.ProfileConfig(profile))
		}
		return secretmask.ChatConfigValue(cfg), nil
	case "provider":
		return secretmask.ChatConfigValue(m.llm.profileSetPayload(m.llm.store.Profiles())), nil
	case "plugin":
		return m.invoke(ctx, profile, target, nil, m.bot.listPlugins)
	case "media_cache":
		return m.invoke(ctx, profile, target, nil, m.cache.get)
	case "history_media":
		return m.invoke(ctx, profile, target, nil, m.media.get)
	case "security":
		return map[string]any{"required": m.auth.manager.Required(), "username": m.auth.manager.Username()}, nil
	case "sessions":
		return m.invoke(ctx, profile, target, nil, m.auth.listSessions)
	case "trusted_apps":
		return m.invoke(ctx, profile, target, nil, m.trusted.settings)
	}
	return nil, fmt.Errorf("资源不存在，请先 catalog")
}
func (m *ConsoleConfigManager) Apply(ctx context.Context, profile, resource, action, target string, payload map[string]any) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, consoleConfigContextKey{}, true)
	// Register newly submitted values before downstream validation can echo them.
	registerConsoleSecrets(payload)
	switch resource {
	case "bot":
		if target == "" {
			target = profile
		}
		if target != profile {
			return nil, fmt.Errorf("请先切换到目标机器人的管理对话")
		}
		switch action {
		case "update":
			_, err := m.bot.SaveBotFields(ctx, profile, func(cfg assistant.BotConfig) (assistant.BotConfig, error) {
				merged, e := consoleMerge(assistant.PayloadFromConfigWithSecrets(cfg), payload)
				if e != nil {
					return cfg, e
				}
				b, _ := json.Marshal(merged)
				var next assistant.ConfigPayload
				if e = json.Unmarshal(b, &next); e != nil {
					return cfg, e
				}
				if e = assistant.ValidateAgentMode(next.AgentMode); e != nil {
					return cfg, e
				}
				out := assistant.ConfigFromPayload(next, cfg)
				return out, out.Validate()
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "message": "机器人设置已保存"}, nil
		case "create":
			merged, e := consoleMerge(assistant.PayloadFromConfig(assistant.DefaultBotConfig()), payload)
			if e != nil {
				return nil, e
			}
			return m.invoke(ctx, profile, "", merged, m.bot.createProfile)
		case "clone":
			return m.invoke(ctx, profile, target, map[string]any{"id": target}, m.bot.cloneProfile)
		case "delete":
			return m.invoke(ctx, profile, target, map[string]any{"id": target}, m.bot.deleteProfile)
		}
	case "group":
		if action != "update" || target == "" {
			return nil, fmt.Errorf("请指定群 ID 并使用 update")
		}
		base := m.bot.runtime.ProfileConfig(profile)
		previous, ok := m.bot.groupConfigs.ConfigForGroup(profile, target)
		if !ok {
			previous = assistant.DefaultGroupConfig(target, base)
		}
		merged, e := consoleMerge(previous, payload)
		if e != nil {
			return nil, e
		}
		data, _ := json.Marshal(merged)
		var next assistant.GroupConfig
		if e = json.Unmarshal(data, &next); e != nil {
			return nil, e
		}
		next.GroupID = target
		next.BotProfileID = profile
		saved, e := m.bot.SaveGroupFields(ctx, next, previous, base, true)
		if e != nil {
			return nil, e
		}
		return secretmask.ChatConfigValue(saved), nil
	case "provider":
		switch action {
		case "reorder":
			return m.invoke(ctx, profile, target, payload, m.llm.reorderProfiles)
		case "create":
			merged, e := consoleMerge(llmConfigPayload{}, payload)
			if e != nil {
				return nil, e
			}
			return m.invoke(ctx, profile, "", merged, m.llm.saveConfig)
		case "update":
			for _, p := range m.llm.store.Profiles().Profiles {
				if p.ID == target {
					merged, e := consoleMerge(payloadFromProfileWithSecrets(p), payload)
					if e != nil {
						return nil, e
					}
					return m.invoke(ctx, profile, target, merged, m.llm.saveConfig)
				}
			}
			return nil, fmt.Errorf("提供商不存在")
		case "clone":
			return m.invoke(ctx, profile, target, map[string]any{"id": target}, m.llm.cloneProfile)
		case "delete":
			return m.invoke(ctx, profile, target, map[string]any{"id": target}, m.llm.deleteProfile)
		}
	case "plugin":
		if state, ok := m.bot.runtime.Plugins().Get(target); ok {
			if settings, ok := payload["settings"].(map[string]any); ok {
				for _, spec := range state.Manifest.Settings {
					if spec.Secret {
						if value, ok := settings[spec.Key].(string); ok {
							secretmask.Register(value)
						}
					}
				}
			}
		}
		if action == "update" {
			return m.invoke(ctx, profile, target, payload, m.bot.updatePluginSettings)
		}
	case "media_cache", "history_media":
		if action != "update" {
			break
		}
		var current any
		var handler gin.HandlerFunc
		if resource == "media_cache" {
			m.cache.mu.Lock()
			current = m.cache.policy
			m.cache.mu.Unlock()
			handler = m.cache.save
		} else {
			m.media.mu.Lock()
			current = m.media.policy
			m.media.mu.Unlock()
			handler = m.media.save
		}
		merged, e := consoleMerge(current, payload)
		if e != nil {
			return nil, e
		}
		return m.invoke(ctx, profile, target, merged, handler)
	case "security":
		if action == "update" {
			username, _ := payload["new_username"].(string)
			password, _ := payload["new_password"].(string)
			if username == "" {
				username = m.auth.manager.Username()
			}
			_, e := m.auth.manager.SetCredentials(username, password)
			if e != nil {
				return nil, e
			}
			return map[string]any{"ok": true, "message": "控制台凭据已更新，需要重新登录"}, nil
		}
	case "sessions":
		if action == "delete" {
			return m.invoke(ctx, profile, target, nil, m.auth.revokeSession)
		}
		if action == "delete_all" {
			return m.invoke(ctx, profile, target, nil, m.auth.revokeOtherSessions)
		}
	case "trusted_apps":
		if action == "enable" {
			return m.invoke(ctx, profile, target, payload, m.trusted.saveEnabled)
		}
		if action == "create" || action == "rotate" {
			secret, _ := payload["secret"].(string)
			if len(secret) < 32 {
				return nil, fmt.Errorf("请提供至少 32 字符的应用密钥并自行保存，聊天不回显密钥")
			}
		}
		switch action {
		case "create":
			return m.invoke(ctx, profile, "", payload, m.trusted.saveApp)
		case "update":
			return m.invoke(ctx, profile, target, payload, m.trusted.saveApp)
		case "rotate":
			return m.invoke(ctx, profile, target, payload, m.trusted.rotateApp)
		}
	}
	return nil, fmt.Errorf("资源或操作不支持，请先 catalog")
}
func registerConsoleSecrets(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if secretmask.SensitiveName(key) {
				if text, ok := item.(string); ok {
					secretmask.Register(text)
				}
			}
			registerConsoleSecrets(item)
		}
	case []any:
		for _, item := range v {
			registerConsoleSecrets(item)
		}
	}
}
func (m *ConsoleConfigManager) invoke(ctx context.Context, profile, target string, payload map[string]any, handler gin.HandlerFunc) (any, error) {
	body, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/internal/chat-settings?profile="+url.QueryEscape(profile), bytes.NewReader(body)).WithContext(context.WithValue(ctx, consoleConfigContextKey{}, true))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Diana-Actor", "chat-settings")
	c.Params = gin.Params{{Key: "id", Value: target}}
	handler(c)
	var out any
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		out = map[string]any{"status": rec.Code}
	}
	out = secretmask.ChatConfigValue(out)
	if rec.Code >= 400 {
		b, _ := json.Marshal(out)
		return nil, fmt.Errorf("保存或读取失败：%s", b)
	}
	return out, nil
}
