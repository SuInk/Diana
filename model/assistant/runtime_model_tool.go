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

// 字段刻意分成三层：模型 ID、供应商、配置名。以前只有一个 provider 字段，里面
// 装的却是配置在控制台里的显示名（「主对话模型」这种），模型照着念，用户问「你用
// 的什么模型」得到的是一个配置名——看起来它知道自己的配置，却说不出模型 ID。
type dianaRuntimeModelResult struct {
	// ModelID 是真正提交给接口的模型标识，问「什么模型」答的就是它。
	ModelID string `json:"model_id"`
	// Provider 是供应商/接口类型（openai_compatible、anthropic、gemini…）。
	Provider string `json:"provider,omitempty"`
	// ConfigName 是这套配置在控制台里的名字，不是模型名。
	ConfigName string `json:"config_name,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	// Group 是模型分组标识，GroupLabel 是它对应的用途。
	Group         string `json:"group"`
	GroupLabel    string `json:"group_label,omitempty"`
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

const dianaRuntimeModelReplyGuidance = "回答「你用的什么模型」时报 model_id 的原文，" +
	"不要拿 config_name 顶替——那只是这套配置在控制台里的名字。对方追问供应商或接口再讲 provider 和 protocol。"

// runtimeModelGroupLabel 把分组标识说成用途。
func runtimeModelGroupLabel(group string) string {
	switch llm.NormalizeProfileGroup(group) {
	case llm.GroupVision:
		return "视觉理解"
	case llm.GroupIntent:
		return "意图识别"
	case llm.GroupImage:
		return "图片生成"
	case llm.GroupChat:
		return "对话"
	default:
		return ""
	}
}

func newDianaRuntimeModelTool(provider *runtimeAgentLLMProvider) *dianaRuntimeModelTool {
	return &dianaRuntimeModelTool{provider: provider}
}

func (*dianaRuntimeModelTool) Name() string { return dianaRuntimeModelToolName }

func (*dianaRuntimeModelTool) Description() string {
	return "读取 Diana 本轮实际使用的模型 ID、供应商、接口协议和模型分组用途。" +
		"仅当用户询问 Diana 当前是什么模型、模型 ID、由哪个供应商提供或使用何种接口时调用；不得根据历史回复猜测。" +
		"注意 model_id 才是模型，config_name 只是这套配置在控制台里的名字。无需参数。"
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
	provider = unwrapTransientLLMRetry(provider)
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
			ModelID:       model.ModelID,
			Provider:      definition.ID,
			ConfigName:    strings.TrimSpace(definition.Name),
			Protocol:      string(definition.Protocol),
			Group:         group,
			GroupLabel:    runtimeModelGroupLabel(group),
			ReplyGuidance: dianaRuntimeModelReplyGuidance,
		}, nil
	case *profileFailoverLLMProvider:
		client.mu.Lock()
		defer client.mu.Unlock()
		if len(client.profiles) == 0 || client.current < 0 || client.current >= len(client.profiles) {
			return dianaRuntimeModelResult{}, fmt.Errorf("当前 Profile 模型不可用")
		}
		profile := client.profiles[client.current]
		return dianaRuntimeModelResult{
			ModelID:       profile.Config.Model,
			Provider:      string(profile.Config.Provider),
			ConfigName:    strings.TrimSpace(profile.Name),
			Protocol:      string(profile.Config.APIFormat),
			Group:         group,
			GroupLabel:    runtimeModelGroupLabel(group),
			ReplyGuidance: dianaRuntimeModelReplyGuidance,
		}, nil
	default:
		return dianaRuntimeModelResult{}, fmt.Errorf("当前 Provider 未公开模型身份")
	}
}
