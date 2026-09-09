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
	return "diana.llm_config"
}

func (t *dianaLLMConfigTool) Description() string {
	return `切换 Diana 各个用途使用的模型：对话、视觉理解、意图识别、图片生成，由 role 指定，默认对话。` +
		`只修改当前消息所属机器人。跨供应商切换时先 list 查看供应商 ID 与模型，再用 provider_id 精确选择；不要猜测同名供应商。` +
		`改的是机器人的模型分配，不动 provider 的地址和密钥。` +
		`只有主人明确要求更改机器人自身配置时才能调用；讨论模型、推荐 API 中转、分析别人的 Agent 或模型、用户说「我用某模型」都不得调用。`
}

func (t *dianaLLMConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("list 查看已配置供应商和缓存模型清单；update 修改当前机器人。", "list", "update"),
		"role": toolEnumParam("要改哪个用途的模型：chat 对话（默认）、vision 视觉理解、intent 意图识别、image 图片生成。"+
			"用户说「识图用 X」「生图换成 Y」「意图判断用 Z」时要传对应的值。",
			"chat", "vision", "intent", "image"),
		"provider":      toolEnumParam("要切换到的 provider，不改则省略。", "openai_compatible", "gemini", "anthropic"),
		"provider_id":   toolStringParam("WebUI 已配置的具体供应商 ID，优先使用 list 返回的 ID。不是机器人 ID。"),
		"provider_name": toolStringParam("供应商的准确名称；重名时必须改用 provider_id。"),
		"model":         toolStringParam("要切换到的模型 ID，不改则省略。"),
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
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "update"
	}
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
		Text:     fmt.Sprintf("diana.llm_config role=%s provider=%s model=%s", command.Role, providerRaw, model),
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
	default:
		return "", fmt.Errorf("不支持的 provider %q", raw)
	}
}
