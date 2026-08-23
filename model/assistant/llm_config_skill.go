// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const llmConfigPluginID = "official.llm-config-skill"

// LLMConfigPlugin keeps the built-in capability visible without interpreting
// or intercepting natural-language messages.
type LLMConfigPlugin struct{}

// NewLLMConfigPlugin creates the built-in LLM configuration capability.
func NewLLMConfigPlugin() *LLMConfigPlugin {
	return &LLMConfigPlugin{}
}

func (p *LLMConfigPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          llmConfigPluginID,
		Name:        "LLM 配置",
		Version:     "0.1.0",
		Description: "官方内置 LLM 配置能力；自然语言由主 Agent 理解，配置修改仅通过主人专属结构化工具执行。",
		Official:    true,
		BuiltIn:     true,
		// 没有可调项，写操作本身已经被主人身份挡住，摆成开关只是噪音。
		Internal:    true,
		Permissions: []string{"message:read", "llm:config:write"},
	}
}

// Handle deliberately does nothing: the main Agent decides whether an owner
// request warrants a structured diana.llm_config tool call.
func (p *LLMConfigPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type llmConfigCommand struct {
	Provider    llm.Provider
	ProviderSet bool
	Model       string
}

type llmConfigApplyResult struct {
	Reply       string
	Updated     bool
	ProfileID   string
	ProfileName string
	OldProvider llm.Provider
	NewProvider llm.Provider
	OldModel    string
	NewModel    string
}

// applyLLMConfigCommand applies validated input from the structured Agent tool.
func applyLLMConfigCommand(ctx context.Context, store LLMProfileStore, command llmConfigCommand, listModels LLMModelLister) llmConfigApplyResult {
	set := store.Profiles().WithDefaults()
	current, ok := set.Current()
	if !ok {
		return llmConfigApplyResult{Reply: "当前没有激活的 LLM 配置。"}
	}
	if listModels == nil {
		listModels = defaultLLMModelLister
	}

	nextCfg := current.Config.WithDefaults()
	oldProvider := nextCfg.Provider
	oldModel := nextCfg.Model
	if command.ProviderSet {
		// 只切 provider 且没指定模型时，换到该 provider 的默认模型，避免保留旧 provider 的无效模型名。
		nextCfg.Provider = command.Provider
		if strings.TrimSpace(command.Model) == "" && oldProvider != command.Provider {
			nextCfg.Model = llm.DefaultModel(command.Provider)
		}
	}
	if strings.TrimSpace(command.Model) != "" {
		nextCfg.Model = strings.TrimSpace(command.Model)
	}
	nextCfg = nextCfg.WithDefaults()
	// 必须先问后端模型列表，防止用户切到 provider 里不存在的模型后导致机器人不可用。
	modelInfo, models, err := ensureLLMModelAvailable(ctx, nextCfg, listModels)
	if err != nil {
		return llmConfigApplyResult{
			Reply:       "更新失败：" + err.Error(),
			ProfileID:   current.ID,
			ProfileName: current.Name,
			OldProvider: oldProvider,
			NewProvider: nextCfg.Provider,
			OldModel:    oldModel,
			NewModel:    nextCfg.Model,
		}
	}
	if len(models) > 0 {
		nextCfg.Models = models
	}
	// 换模型时不再把目录返回的窗口写进配置。窗口是模型的属性，一个 provider 下面
	// 挂着几十个模型；写进配置就等于把「某一刻从第三方目录读到的数」固定成用户设置，
	// 之后换模型不跟着变，目录改了也不跟着变。读取时按当前模型现算即可，
	// 用户手填的值仍然优先（见 llm.ResolveContextWindowTokens）。
	window := nextCfg.ContextWindowTokensWithDefault()
	if modelInfo.ContextWindowTokens > 0 {
		window = modelInfo.ContextWindowTokens
	}
	if nextCfg.ContextWindowTokens > 0 {
		window = nextCfg.ContextWindowTokens
	}
	// 用户设过的请求上限要留住，但不能超过新模型的窗口。
	if nextCfg.MaxContextTokens > window {
		nextCfg.MaxContextTokens = window
	}
	if modelInfo.MaxOutputTokens > 0 && nextCfg.MaxOutputTokens > modelInfo.MaxOutputTokens {
		nextCfg.MaxOutputTokens = modelInfo.MaxOutputTokens
	}
	if budget := nextCfg.MaxContextTokensWithDefault(); nextCfg.MaxOutputTokens >= budget {
		nextCfg.MaxOutputTokens = budget / 4
	}
	if err := nextCfg.Validate(); err != nil {
		return llmConfigApplyResult{
			Reply:       "更新失败：" + err.Error(),
			ProfileID:   current.ID,
			ProfileName: current.Name,
			OldProvider: oldProvider,
			NewProvider: nextCfg.Provider,
			OldModel:    oldModel,
			NewModel:    nextCfg.Model,
		}
	}

	for i := range set.Profiles {
		if set.Profiles[i].ID != current.ID {
			continue
		}
		set.Profiles[i].Config = nextCfg
		set.ActiveID = current.ID
		if err := store.SaveProfiles(set); err != nil {
			return llmConfigApplyResult{Reply: fmt.Sprintf("更新失败：配置没能写入存储（%v）。", err)}
		}
		return llmConfigApplyResult{
			Reply:       fmt.Sprintf("已更新当前 LLM：%s\nProvider：%s -> %s\nModel：%s -> %s", current.Name, oldProvider, nextCfg.Provider, oldModel, nextCfg.Model),
			Updated:     true,
			ProfileID:   current.ID,
			ProfileName: current.Name,
			OldProvider: oldProvider,
			NewProvider: nextCfg.Provider,
			OldModel:    oldModel,
			NewModel:    nextCfg.Model,
		}
	}
	return llmConfigApplyResult{Reply: "当前没有激活的 LLM 配置。"}
}

// recordLLMConfigSkillLog 记录聊天修改 LLM 配置的审计日志。
func recordLLMConfigSkillLog(ctx context.Context, req PluginRequest, result llmConfigApplyResult, err error) {
	if req.AppLogs == nil {
		return
	}
	// 聊天修改 LLM 配置会影响运行时行为，所以和 WebUI 配置变更写入同一条审计流。
	// 成功算操作日志，被拒绝或失败算错误日志，操作者记录为用户。
	kind := applog.KindError
	level := applog.LevelError
	message := result.Reply
	if result.Updated {
		kind = applog.KindOperation
		level = applog.LevelInfo
		message = "聊天修改 LLM 配置成功"
	}
	metadata := map[string]any{
		"user_id": req.Event.UserID,
		"kind":    string(req.Event.Kind),
		"command": req.Text,
	}
	if req.Event.GroupID != "" {
		metadata["group_id"] = req.Event.GroupID
	}
	if result.ProfileID != "" {
		metadata["profile_id"] = result.ProfileID
	}
	if result.ProfileName != "" {
		metadata["profile_name"] = result.ProfileName
	}
	if result.OldProvider != "" {
		metadata["old_provider"] = string(result.OldProvider)
	}
	if result.NewProvider != "" {
		metadata["new_provider"] = string(result.NewProvider)
	}
	if result.OldModel != "" {
		metadata["old_model"] = result.OldModel
	}
	if result.NewModel != "" {
		metadata["new_model"] = result.NewModel
	}
	detail := result.Reply
	if err != nil {
		detail = err.Error()
	}
	// 审计日志失败不影响聊天命令结果；有效 owner 命令不能因为日志系统异常而失败。
	_ = req.AppLogs.AppendLog(ctx, applog.Entry{
		Kind:     kind,
		Level:    level,
		Action:   "assistant.llm_config.command",
		Message:  message,
		Detail:   detail,
		Actor:    oneBotEventActor(req.Event),
		Target:   firstNonEmpty(result.ProfileID, result.NewModel, result.OldModel),
		Metadata: metadata,
	})
}

// oneBotEventActor 将 OneBot 事件转换为日志操作者标识。
func oneBotEventActor(event MessageEvent) string {
	// 给 actor 加命名空间，日志中心里能区分 WebUI 操作者和用户。
	if userID := strings.TrimSpace(event.UserID); userID != "" {
		return "qq:" + userID
	}
	return "qq:unknown"
}

// defaultLLMModelLister 使用默认 LLM 模型列表实现。
func defaultLLMModelLister(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
	return llm.ListModels(ctx, cfg)
}

// ensureLLMModelAvailable 校验目标模型是否存在于 provider 后端列表。
// ensureLLMModelAvailable 校验模型可用，并把这次拿到的完整模型清单一起带回。
// 清单要跟着写回配置：切了 provider 之后旧清单就不再对应当前后端，留着它会让
// 「按模型清单取窗口」查到上一个 provider 的条目。
func ensureLLMModelAvailable(ctx context.Context, cfg llm.ProviderConfig, listModels LLMModelLister) (llm.ModelInfo, []llm.ModelInfo, error) {
	model := strings.TrimSpace(cfg.Model)
	// listModels 会走当前 provider 的真实后端接口；不能靠本地硬编码模型名判断。
	models, err := listModels(ctx, cfg)
	if err != nil {
		return llm.ModelInfo{}, nil, fmt.Errorf("无法读取 %s 的模型列表，未保存；请先在 WebUI 的模型列表里选择可用模型。%v", cfg.Provider, err)
	}
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate.ID), model) {
			return candidate, models, nil
		}
	}
	return llm.ModelInfo{}, nil, fmt.Errorf("模型 %s 不在 %s 的模型列表中，未保存。可选：%s", model, cfg.Provider, summarizeModelIDs(models))
}

// summarizeModelIDs 摘要展示可选模型 ID。
func summarizeModelIDs(models []llm.ModelInfo) string {
	ids := make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= 8 {
			break
		}
	}
	if len(ids) == 0 {
		return "暂无可用模型"
	}
	return strings.Join(ids, "、")
}
