// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	return `切换 Diana 各用途的模型（role 指定，默认对话），只改当前机器人，不动供应商地址和密钥。` +
		`仅在主人明确要求改机器人自身配置时调用；讨论或推荐模型、用户说「我用某模型」都不调用。` +
		`跨供应商先 list，再用 provider_id 精确选，别猜同名供应商。更新前实测目标模型，失败保留原配置；image 会生成一张不发送的测试图。`
}

func (t *dianaLLMConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("list 看供应商和模型清单，update 修改当前机器人。", "list", "update"),
		"role": toolEnumParam("chat 对话（默认）、vision 识图、intent 意图识别、image 生图。",
			"chat", "vision", "intent", "image"),
		"provider":      toolEnumParam("目标 provider 类型，不改则省略。", "openai_compatible", "gemini", "anthropic", "typesafe"),
		"provider_id":   toolStringParam("供应商 ID（不是机器人 ID），取自 list。"),
		"provider_name": toolStringParam("供应商准确名称；重名时改用 provider_id。"),
		"model":         toolStringParam("目标模型 ID，不改则省略。"),
	})
}

func (t *dianaLLMConfigTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana LLM config: runtime is not configured")
	}
	cfg, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	if !cfg.IsOwnerEvent(t.event) {
		return "", fmt.Errorf("只有主人可以修改提供商配置")
	}
	if !boolValue(cfg.OwnerLLMConfigEnabled, true) {
		return "", fmt.Errorf("当前机器人已关闭聊天模型配置")
	}
	operation := t.CanonicalOperation(input)
	if operation == "list" {
		return t.listProviders(cfg)
	}
	if operation != "update" {
		return "", fmt.Errorf("operation 必须是 list 或 update")
	}
	providerRaw := strings.ToLower(strings.TrimSpace(configToolString(input, "provider")))
	model := strings.TrimSpace(configToolString(input, "model"))
	providerID := strings.TrimSpace(configToolString(input, "provider_id"))
	providerName := strings.TrimSpace(configToolString(input, "provider_name"))
	if providerRaw == "" && model == "" && providerID == "" && providerName == "" {
		return "", fmt.Errorf("至少提供 provider 或 model")
	}
	command := llmConfigCommand{Model: model, Role: strings.TrimSpace(configToolString(input, "role")), ProviderID: providerID, ProviderName: providerName}
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
