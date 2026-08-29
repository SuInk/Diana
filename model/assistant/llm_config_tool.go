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
		`改的是机器人的模型分配，不动 provider 的地址和密钥。` +
		`只有主人明确要求更改机器人自身配置时才能调用；讨论模型、推荐 API 中转、分析别人的 Agent 或模型、用户说「我用某模型」都不得调用。`
}

func (t *dianaLLMConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。", "update"),
		"role": toolEnumParam("要改哪个用途的模型：chat 对话（默认）、vision 视觉理解、intent 意图识别、image 图片生成。"+
			"用户说「识图用 X」「生图换成 Y」「意图判断用 Z」时要传对应的值。",
			"chat", "vision", "intent", "image"),
		"provider": toolEnumParam("要切换到的 provider，不改则省略。", "openai_compatible", "gemini", "anthropic"),
		"model":    toolStringParam("要切换到的模型 ID，不改则省略。"),
	})
}

func (t *dianaLLMConfigTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana LLM config: runtime is not configured")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只有主人可以修改提供商配置")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "update"
	}
	if operation != "update" {
		return "", fmt.Errorf("operation 必须是 update")
	}
	providerRaw := strings.ToLower(strings.TrimSpace(configToolString(input, "provider")))
	model := strings.TrimSpace(configToolString(input, "model"))
	if providerRaw == "" && model == "" {
		return "", fmt.Errorf("至少提供 provider 或 model")
	}
	command := llmConfigCommand{Model: model, Role: strings.TrimSpace(configToolString(input, "role"))}
	if providerRaw != "" {
		provider, err := structuredLLMProvider(providerRaw)
		if err != nil {
			return "", err
		}
		command.Provider = provider
		command.ProviderSet = true
	}
	if t.runtime.llmStore == nil {
		return "", fmt.Errorf("当前未接入提供商配置集")
	}
	result := t.runtime.applyLLMConfigCommand(ctx, command, t.runtime.llmModelLister())
	recordLLMConfigSkillLog(ctx, PluginRequest{
		Event:    t.event,
		Text:     fmt.Sprintf("diana.llm_config role=%s provider=%s model=%s", command.Role, providerRaw, model),
		OwnerID:  t.runtime.effectiveConfigForEvent(t.event).OwnerID,
		LLMStore: t.runtime.llmStore,
		AppLogs:  t.runtime.appLogWriter(),
	}, result, nil)
	if !result.Updated {
		return "", fmt.Errorf("%s", result.Reply)
	}
	body, err := json.Marshal(map[string]any{
		"ok":           true,
		"action":       "updated",
		"role":         result.Role,
		"message":      result.Reply,
		"profile_id":   result.ProfileID,
		"profile_name": result.ProfileName,
		"old_provider": result.OldProvider,
		"new_provider": result.NewProvider,
		"old_model":    result.OldModel,
		"new_model":    result.NewModel,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
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
