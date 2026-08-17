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

const dianaRuntimeModelToolName = "diana.runtime_model"

type dianaRuntimeModelTool struct {
	provider *runtimeAgentLLMProvider
}

type dianaRuntimeModelResult struct {
	Provider string `json:"provider"`
	Protocol string `json:"protocol,omitempty"`
	Model    string `json:"model"`
	Group    string `json:"group"`
}

func newDianaRuntimeModelTool(provider *runtimeAgentLLMProvider) *dianaRuntimeModelTool {
	return &dianaRuntimeModelTool{provider: provider}
}

func (*dianaRuntimeModelTool) Name() string { return dianaRuntimeModelToolName }

func (*dianaRuntimeModelTool) Description() string {
	return "读取 Diana 本轮实际使用的模型、Provider、接口协议和模型分组。仅当用户询问 Diana 当前是什么模型、由哪个 Provider 提供或使用何种模型接口时调用；不得根据历史回复猜测。无需参数。"
}

func (t *dianaRuntimeModelTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t == nil || t.provider == nil {
		return "", fmt.Errorf("diana runtime model: provider is not configured")
	}
	identity, err := t.provider.currentModelIdentity()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("编码当前模型信息: %w", err)
	}
	return string(body), nil
}

func (p *runtimeAgentLLMProvider) currentModelIdentity() (dianaRuntimeModelResult, error) {
	if p == nil {
		return dianaRuntimeModelResult{}, fmt.Errorf("diana runtime model: provider is not configured")
	}
	p.mu.Lock()
	group := llm.NormalizeProfileGroup(p.lastGroup)
	provider := p.providers[group]
	p.mu.Unlock()
	if provider == nil {
		return dianaRuntimeModelResult{}, fmt.Errorf("当前模型尚未完成首次调用")
	}
	switch client := provider.(type) {
	case llm.RegistryClient:
		model, ok := client.Registry.Model(client.Selection.ModelID)
		if !ok {
			return dianaRuntimeModelResult{}, fmt.Errorf("当前 Registry 模型 %q 不存在", client.Selection.ModelID)
		}
		definition, ok := client.Registry.Provider(model.ProviderID)
		if !ok {
			return dianaRuntimeModelResult{}, fmt.Errorf("当前 Registry Provider %q 不存在", model.ProviderID)
		}
		return dianaRuntimeModelResult{
			Provider: firstNonEmpty(strings.TrimSpace(definition.Name), definition.ID),
			Protocol: string(definition.Protocol),
			Model:    model.ModelID,
			Group:    group,
		}, nil
	case *profileFailoverLLMProvider:
		client.mu.Lock()
		defer client.mu.Unlock()
		if len(client.profiles) == 0 || client.current < 0 || client.current >= len(client.profiles) {
			return dianaRuntimeModelResult{}, fmt.Errorf("当前 Profile 模型不可用")
		}
		profile := client.profiles[client.current]
		return dianaRuntimeModelResult{
			Provider: firstNonEmpty(strings.TrimSpace(profile.Name), string(profile.Config.Provider)),
			Protocol: string(profile.Config.APIFormat),
			Model:    profile.Config.Model,
			Group:    group,
		}, nil
	default:
		return dianaRuntimeModelResult{}, fmt.Errorf("当前 Provider 未公开模型身份")
	}
}
