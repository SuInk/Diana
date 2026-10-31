// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/llm"
)

type dianaLLMConfigTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaLLMConfigTool(runtime *Runtime, event MessageEvent) *dianaLLMConfigTool {
	return &dianaLLMConfigTool{runtime: runtime, event: event}
}

func (t *dianaLLMConfigTool) Name() string {
	return "llm_config"
}

func (t *dianaLLMConfigTool) Description() string {
	return `管理 Diana 模型：切换、启用、停用和调度策略（role 默认对话），不动供应商地址和密钥。` +
		`仅在主人明确要求改机器人自身配置时调用；讨论或推荐模型、用户说「我用某模型」都不调用。` +
		`用户说本群、这个群时必须用 scope=group；说整个机器人时用 scope=bot。恢复本群跟随机器人用 operation=reset、scope=group。` +
		`启用/停用已有路由用 enable/disable，必须先 list 确认 model 和 provider_id（分组路由用 group）；同名时不要猜。切换调度策略用 set_strategy 和 routing_strategy。这三项仅支持 scope=bot，不调整模型顺序，也不测试上游。` +
		`切换只将目标模型排到首位，保留其他模型与后备；不在列表的目标添加到首位。` +
		`跨供应商先 list，再用 provider_id 精确选，别猜同名供应商。更新前实测目标模型，失败保留原配置；image 会生成一张不发送的测试图。`
}

func (t *dianaLLMConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("list 看供应商和模型清单，update 修改指定范围，reset 恢复本群跟随机器人；enable/disable 启停已有模型；set_strategy 切换调度策略。", "list", "update", "reset", "enable", "disable", "set_strategy"),
		"scope":     toolEnumParam("group 仅当前群（仅 chat）；bot 整个机器人（默认）。", "group", "bot"),
		"role": toolEnumParam("chat 对话（默认）、vision 识图、intent 意图识别、image 生图。",
			"chat", "vision", "intent", "image"),
		"provider":         toolEnumParam("目标 provider 类型，不改则省略。", "openai_compatible", "gemini", "anthropic", "typesafe", "openai_decisions"),
		"provider_id":      toolStringParam("供应商 ID（不是机器人 ID），取自 list。"),
		"provider_name":    toolStringParam("供应商准确名称；重名时改用 provider_id。"),
		"model":            toolStringParam("目标模型 ID，不改则省略。"),
		"group":            toolStringParam("启停分组路由时填写 list 中的准确分组名，不能和 provider_id 混用。"),
		"routing_strategy": toolEnumParam("set_strategy 必填：smart 智能、failover 主备、round_robin 轮询、weighted 加权；image 不支持 smart。", "smart", "failover", "round_robin", "weighted"),
	})
}

func (t *dianaLLMConfigTool) Run(ctx context.Context, input map[string]any) (out string, runErr error) {
	ctx = secretmask.WithChatConfig(ctx)
	defer func() {
		out = secretmask.ChatConfigJSON(out)
		if runErr != nil {
			runErr = secretmask.ChatConfigError(runErr)
		}
	}()

	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana LLM config: runtime is not configured")
	}
	cfg, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	if !cfg.IsManagerEvent(t.event) {
		return "", fmt.Errorf("只有主人或管理员可以修改提供商配置")
	}
	if !boolValue(cfg.OwnerLLMConfigEnabled, true) {
		return "", fmt.Errorf("当前机器人已关闭聊天模型配置")
	}
	operation := t.CanonicalOperation(input)
	if operation == "list" {
		return t.listProviders(cfg)
	}
	if operation == "reset" {
		if configToolString(input, "scope") != "group" || (configToolString(input, "role") != "" && configToolString(input, "role") != "chat") {
			return "", fmt.Errorf("reset 仅支持 scope=group、role=chat")
		}
		if err := t.runtime.saveGroupChatModel(t.event, GroupChatModel{}); err != nil {
			return "", err
		}
		return `{"ok":true,"scope":"group","message":"当前群已恢复跟随机器人的聊天模型"}`, nil
	}
	if operation == "enable" || operation == "disable" || operation == "set_strategy" {
		reply, err := t.configureRoutes(ctx, input, operation)
		result := llmConfigApplyResult{Updated: err == nil, Role: configToolString(input, "role"), Reply: reply}
		if err != nil {
			result.Reply = err.Error()
		}
		recordLLMConfigSkillLog(ctx, PluginRequest{Event: t.event, Text: fmt.Sprintf("llm_config operation=%s role=%s model=%s strategy=%s", operation, result.Role, configToolString(input, "model"), configToolString(input, "routing_strategy")), AppLogs: t.runtime.appLogWriter()}, result, err)
		return reply, err
	}
	if operation != "update" {
		return "", fmt.Errorf("operation 必须是 list、update、reset、enable、disable 或 set_strategy")
	}
	providerRaw := strings.ToLower(strings.TrimSpace(configToolString(input, "provider")))
	model := strings.TrimSpace(configToolString(input, "model"))
	providerID := strings.TrimSpace(configToolString(input, "provider_id"))
	providerName := strings.TrimSpace(configToolString(input, "provider_name"))
	if providerRaw == "" && model == "" && providerID == "" && providerName == "" {
		return "", fmt.Errorf("至少提供 provider 或 model")
	}
	command := llmConfigCommand{Scope: strings.TrimSpace(configToolString(input, "scope")), Model: model, Role: strings.TrimSpace(configToolString(input, "role")), ProviderID: providerID, ProviderName: providerName}
	if providerRaw != "" {
		provider, err := structuredLLMProvider(providerRaw)
		if err != nil {
			return "", err
		}
		command.Provider = provider
		command.ProviderSet = true
	}
	result := t.runtime.applyLLMConfigCommand(ctx, t.event, command, t.runtime.llmModelLister())
	recordLLMConfigSkillLog(ctx, PluginRequest{
		Event:    t.event,
		Text:     fmt.Sprintf("llm_config role=%s provider=%s model=%s", command.Role, providerRaw, model),
		OwnerID:  t.runtime.effectiveConfigForEvent(t.event).OwnerIDForEvent(t.event),
		LLMStore: t.runtime.llmStore,
		AppLogs:  t.runtime.appLogWriter(),
	}, result, nil)
	if !result.Updated {
		return "", fmt.Errorf("%s", result.Reply)
	}
	body, err := json.Marshal(map[string]any{
		"ok":             true,
		"scope":          command.Scope,
		"group_id":       t.event.GroupID,
		"bot_profile_id": cfg.ID,
		"action":         "updated",
		"tested":         true,
		"role":           result.Role,
		"message":        result.Reply,
		"profile_id":     result.ProfileID,
		"provider_id":    result.ProfileID,
		"profile_name":   result.ProfileName,
		"old_provider":   result.OldProvider,
		"new_provider":   result.NewProvider,
		"old_model":      result.OldModel,
		"new_model":      result.NewModel,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaLLMConfigTool) listProviders(cfg BotConfig) (string, error) {
	t.runtime.mu.RLock()
	store := t.runtime.llmStore
	t.runtime.mu.RUnlock()
	if store == nil {
		return "", fmt.Errorf("当前未接入提供商配置集")
	}
	type providerOption struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Protocol llm.Provider `json:"protocol"`
		Models   []string     `json:"models"`
	}
	items := []providerOption{}
	for _, profile := range store.Profiles().WithDefaults().Profiles {
		item := providerOption{ID: profile.ID, Name: profile.Name, Protocol: profile.Config.Provider, Models: []string{}}
		if profile.Config.Model != "" {
			item.Models = append(item.Models, profile.Config.Model)
		}
		for _, model := range profile.Config.Models {
			if model.ID != "" {
				item.Models = appendUniqueStrings(item.Models, model.ID)
			}
		}
		items = append(items, item)
	}
	body, err := json.Marshal(map[string]any{"bot_profile_id": cfg.ID, "providers": items, "model_roles": cfg.ModelRoles})
	return string(body), err
}

func structuredLLMProvider(raw string) (llm.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "openai", "openai_compatible", "openai-compatible":
		return llm.ProviderOpenAICompatible, nil
	case "gemini", "google", "google_genai":
		return llm.ProviderGemini, nil
	case "anthropic", "claude":
		return llm.ProviderAnthropic, nil
	case "openai_decisions", "openai-decisions":
		return llm.ProviderOpenAIDecisions, nil
	case "typesafe", "jev", "typesafe_systemone":
		return llm.ProviderTypeSafe, nil
	default:
		return "", fmt.Errorf("不支持的 provider %q", raw)
	}
}

// CanonicalOperation 是 Run 实际执行的操作：没写 operation 按 update 算。
func (t *dianaLLMConfigTool) CanonicalOperation(input map[string]any) string {
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		return "update"
	}
	return operation
}

// configureRoutes only changes existing bot routes; it never probes or creates a provider.
func (t *dianaLLMConfigTool) configureRoutes(ctx context.Context, input map[string]any, operation string) (string, error) {
	scope := strings.TrimSpace(configToolString(input, "scope"))
	if scope != "" && scope != "bot" {
		return "", fmt.Errorf("启停模型和切换调度策略仅支持 scope=bot，不能修改当前群的独立策略")
	}
	roleKey, ok := normalizeLLMConfigRole(configToolString(input, "role"))
	if !ok {
		return "", fmt.Errorf("不认识的模型用途")
	}
	model := strings.TrimSpace(configToolString(input, "model"))
	providerID := strings.TrimSpace(configToolString(input, "provider_id"))
	group := strings.TrimSpace(configToolString(input, "group"))
	strategy := strings.TrimSpace(configToolString(input, "routing_strategy"))
	if configToolString(input, "provider") != "" || configToolString(input, "provider_name") != "" {
		return "", fmt.Errorf("请先 list，再用准确的 provider_id 或 group 指定已有路由")
	}
	if operation == "set_strategy" {
		if model != "" || providerID != "" || group != "" {
			return "", fmt.Errorf("set_strategy 只接受用途和调度策略，不同时修改模型")
		}
		switch strategy {
		case "smart", "failover", "round_robin", "weighted":
		default:
			return "", fmt.Errorf("routing_strategy 必须是 smart、failover、round_robin 或 weighted")
		}
		if roleKey == "image" && strategy == "smart" {
			return "", fmt.Errorf("图片生成不支持智能调度")
		}
	} else if model == "" || (providerID != "" && group != "") || strategy != "" {
		return "", fmt.Errorf("启停必须填写 model；provider_id 和 group 不能同时填写，也不能同时修改策略")
	}
	t.runtime.modelConfigMu.Lock()
	defer t.runtime.modelConfigMu.Unlock()
	cfg, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	if !cfg.IsManagerEvent(t.event) || !boolValue(cfg.OwnerLLMConfigEnabled, true) {
		return "", fmt.Errorf("当前发送者无权修改此机器人的模型配置")
	}
	roles := normalizeModelRoles(cfg.ModelRoles)
	role, exists := roles[roleKey]
	if !exists || role.FollowChat {
		return "", fmt.Errorf("此用途没有独立模型列表，请明确指定它跟随的用途")
	}
	message := ""
	if operation == "set_strategy" {
		role.RoutingStrategy = strategy
		labels := map[string]string{"smart": "智能", "failover": "主备", "round_robin": "轮询", "weighted": "加权"}
		message = fmt.Sprintf("已将%s模型调度策略切换为%s，模型顺序、启用状态、权重和各自的思考强度保持不变。", llmConfigRoleLabel(roleKey), labels[strategy])
	} else {
		routes := append([]ModelRole{role}, role.Fallbacks...)
		matched := -1
		for i, route := range routes {
			if route.Model != model && route.ModelID != model {
				continue
			}
			if providerID != "" && (route.Group != "" || modelRoleProfileID(route) != providerID) {
				continue
			}
			if group != "" && route.Group != group {
				continue
			}
			if matched >= 0 {
				return "", fmt.Errorf("模型匹配多条路由，请先 list 并明确 provider_id 或 group；重复配置请在 WebUI 编辑")
			}
			matched = i
		}
		if matched < 0 {
			return "", fmt.Errorf("未找到匹配的已配置模型路由，请先 list")
		}
		disabled := operation == "disable"
		if matched == 0 {
			role.Disabled = disabled
		} else {
			role.Fallbacks[matched-1].Disabled = disabled
		}
		verb := "启用"
		if disabled {
			verb = "停用"
		}
		message = fmt.Sprintf("已%s%s模型 %s（第 %d 条路由），顺序和其他模型设置保持不变。", verb, llmConfigRoleLabel(roleKey), model, matched+1)
		enabled := !role.Disabled
		for _, fallback := range role.Fallbacks {
			enabled = enabled || !fallback.Disabled
		}
		if !enabled {
			message += "此用途已没有启用的模型，后续调用将不可用；可在 WebUI 重新启用。"
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := t.runtime.saveModelRoleAssignmentLocked(cfg, roleKey, role); err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{"ok": true, "scope": "bot", "bot_profile_id": cfg.ID, "operation": operation, "role": roleKey, "model_role": role, "tested": false, "message": message})
	return string(body), err
}
