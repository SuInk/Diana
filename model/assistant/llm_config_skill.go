// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// applyLLMConfigCommand 把「换个模型」落到这台机器人的对话模型绑定上。
//
// 改的是 BotConfig.ModelRoles["chat"]，不是 LLM 配置本身。两者管的是不同的事：
// provider 的地址、密钥、默认模型属于「这个 provider 怎么连」，归 WebUI 的 LLM
// 配置页；「这台机器人用哪个模型说话」属于机器人配置，也就是模型分配。
//
// 以前这里改的是激活配置的默认模型。只要配过模型分配，roleBoundProfiles 就会用
// role.Model 覆盖它——回执说「已更新 Model：old -> new」，对话实际还在用旧模型。
// 一个会说谎的成功回执比失败更难查。
//
// 没配过任何模型分配的部署仍然走老路（改激活配置的默认模型）：那种部署里激活
// 配置的默认模型就是它实际在用的，凭空写一条 chat 绑定反而会把识图、意图这些
// 未分配的用途一起拽到对话模型上（它们的回落顺序是 roles[用途] -> roles["chat"]）。
func (r *Runtime) applyLLMConfigCommand(ctx context.Context, command llmConfigCommand, listModels LLMModelLister) llmConfigApplyResult {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return llmConfigApplyResult{Reply: "当前未接入 LLM 配置集。"}
	}
	if listModels == nil {
		listModels = defaultLLMModelLister
	}
	set := store.Profiles().WithDefaults()
	botCfg := r.Config().WithDefaults()
	roles := normalizeModelRoles(botCfg.ModelRoles)

	boundProfile, boundModel, ok := chatModelBinding(set, roles)
	if !ok {
		return llmConfigApplyResult{Reply: "当前没有可用的 LLM 配置。"}
	}
	oldProvider := boundProfile.Config.Provider
	oldModel := boundModel

	target, model, err := resolveLLMConfigTarget(set, boundProfile, boundModel, command)
	if err != nil {
		return llmConfigApplyResult{Reply: "更新失败：" + err.Error(), OldProvider: oldProvider, OldModel: oldModel}
	}

	probe := target.Config.WithDefaults()
	probe.Model = model
	// 必须先问后端模型列表，防止切到 provider 里不存在的模型后机器人直接不可用。
	modelInfo, models, err := ensureLLMModelAvailable(ctx, probe, listModels)
	if err != nil {
		return llmConfigApplyResult{
			Reply:       "更新失败：" + err.Error(),
			ProfileID:   target.ID,
			ProfileName: target.Name,
			OldProvider: oldProvider,
			NewProvider: probe.Provider,
			OldModel:    oldModel,
			NewModel:    model,
		}
	}
	notes := llmConfigOutputTokenNote(target.Config, modelInfo)

	result := llmConfigApplyResult{
		Updated:     true,
		ProfileID:   target.ID,
		ProfileName: target.Name,
		OldProvider: oldProvider,
		NewProvider: probe.Provider,
		OldModel:    oldModel,
		NewModel:    model,
	}
	if len(roles) == 0 {
		// 顺带把这次列到的模型清单存回去：窗口是按「清单里这个模型的窗口」现算的，
		// 清单越新算得越准。这不是用户设置，是缓存下来的 provider 事实。
		if err := saveLLMProfileModel(store, set, target.ID, probe.Provider, model, models); err != nil {
			return llmConfigApplyResult{Reply: "更新失败：配置没能写入存储（" + err.Error() + "）。"}
		}
		result.Reply = fmt.Sprintf("已把对话模型换成 %s（配置：%s）。这个部署没有配模型分配，改的是激活配置的默认模型。%s", model, target.Name, notes)
		return result
	}
	if err := r.saveChatModelRole(botCfg, roles, target, model); err != nil {
		return llmConfigApplyResult{Reply: "更新失败：机器人配置没能保存（" + err.Error() + "）。"}
	}
	result.Reply = fmt.Sprintf("已把对话模型换成 %s（配置：%s）。改的是机器人的模型分配，没有动 LLM 配置里的 provider 设置；识图、意图这些用途各自的分配保持不变。%s", model, target.Name, notes)
	return result
}

// chatModelBinding 报出「这台机器人现在用哪套配置的哪个模型说话」。
// 顺序和 roleBoundProfiles 一致：chat 绑定优先，没绑才是激活配置。
func chatModelBinding(set llm.ProfileSet, roles map[string]ModelRole) (llm.Profile, string, bool) {
	if role, ok := roles["chat"]; ok {
		var candidates []llm.Profile
		if role.Group != "" {
			candidates = set.GroupProfiles(role.Group)
		} else {
			for _, profile := range set.Profiles {
				if profile.ID == role.ProfileID {
					candidates = []llm.Profile{profile}
					break
				}
			}
		}
		if len(candidates) > 0 {
			return candidates[0], role.Model, true
		}
	}
	current, ok := set.Current()
	if !ok {
		return llm.Profile{}, "", false
	}
	return current, current.Config.WithDefaults().Model, true
}

// resolveLLMConfigTarget 选出这次要绑到哪套配置的哪个模型。
func resolveLLMConfigTarget(set llm.ProfileSet, bound llm.Profile, boundModel string, command llmConfigCommand) (llm.Profile, string, error) {
	target := bound
	model := strings.TrimSpace(command.Model)
	if command.ProviderSet && command.Provider != bound.Config.Provider {
		match, ok := firstProfileForProvider(set, command.Provider)
		if !ok {
			return llm.Profile{}, "", fmt.Errorf("WebUI 里没有配置 %s 的 provider，请先添加再切换", command.Provider)
		}
		target = match
		if model == "" {
			model = llm.DefaultModel(command.Provider)
		}
	}
	if model == "" {
		model = boundModel
	}
	if model == "" {
		return llm.Profile{}, "", fmt.Errorf("没有指定要用的模型")
	}
	// 只报了模型名时，如果当前这套配置的模型清单里没有它、而别的配置有，就跟着换过去：
	// 用户说的是模型，不该逼他先想清楚这个模型挂在哪套配置下。
	if !command.ProviderSet && !profileOffersModel(target, model) {
		if match, ok := profileOfferingModel(set, model); ok {
			target = match
		}
	}
	return target, model, nil
}

func firstProfileForProvider(set llm.ProfileSet, provider llm.Provider) (llm.Profile, bool) {
	for _, profile := range set.Profiles {
		if profile.Config.WithDefaults().Provider == provider {
			return profile, true
		}
	}
	return llm.Profile{}, false
}

// profileOffersModel 只看已经同步下来的模型清单；清单为空时不做判断（当作可能支持）。
func profileOffersModel(profile llm.Profile, model string) bool {
	supported, known := profileSupportsRoleModel(profile, model)
	return !known || supported
}

func profileOfferingModel(set llm.ProfileSet, model string) (llm.Profile, bool) {
	for _, profile := range set.Profiles {
		if supported, known := profileSupportsRoleModel(profile, model); known && supported {
			return profile, true
		}
	}
	return llm.Profile{}, false
}

// llmConfigOutputTokenNote 在新模型的输出上限比配置里填的还小时给一句提醒。
// 这里不代改：最大输出 Token 是 provider 配置，归 WebUI。
func llmConfigOutputTokenNote(cfg llm.ProviderConfig, info llm.ModelInfo) string {
	if info.MaxOutputTokens <= 0 || cfg.MaxOutputTokens <= 0 || cfg.MaxOutputTokens <= info.MaxOutputTokens {
		return ""
	}
	return fmt.Sprintf("提醒：这套配置的最大输出 Token 填的是 %d，超过新模型的 %d，需要在 WebUI 的 LLM 配置里调低。", cfg.MaxOutputTokens, info.MaxOutputTokens)
}

// saveChatModelRole 只改 chat 这一档绑定，其余用途原样保留。
func (r *Runtime) saveChatModelRole(botCfg BotConfig, roles map[string]ModelRole, target llm.Profile, model string) error {
	next := make(map[string]ModelRole, len(roles)+1)
	for key, role := range roles {
		next[key] = role
	}
	role := next["chat"]
	// 原来按分组绑定、而新配置仍在那个分组里时保留分组绑定，只换模型：
	// 分组绑定带故障转移，改成单配置会把这个能力弄丢。
	if role.Group != "" && profileInGroup(target, role.Group) {
		role.Model = model
	} else {
		role = ModelRole{ProfileID: target.ID, Model: model}
	}
	next["chat"] = role
	botCfg.ModelRoles = normalizeModelRoles(next)
	botCfg = botCfg.WithDefaults()
	r.mu.Lock()
	r.cfg = botCfg
	r.updatedAt = time.Now()
	saver := r.configSaver
	r.mu.Unlock()
	if saver == nil {
		return fmt.Errorf("当前部署没有接入机器人配置存储")
	}
	// 聊天里改的配置必须立刻落盘，否则重启就丢。
	saver.SaveBotConfig(botCfg)
	return nil
}

func profileInGroup(profile llm.Profile, group string) bool {
	return llm.NormalizeProfileGroup(profile.Group) == llm.NormalizeProfileGroup(group)
}

// saveLLMProfileModel 是没有模型分配时的老路：直接改激活配置的默认模型。
func saveLLMProfileModel(store LLMProfileStore, set llm.ProfileSet, profileID string, provider llm.Provider, model string, models []llm.ModelInfo) error {
	for i := range set.Profiles {
		if set.Profiles[i].ID != profileID {
			continue
		}
		cfg := set.Profiles[i].Config.WithDefaults()
		cfg.Provider = provider
		cfg.Model = model
		if len(models) > 0 {
			cfg.Models = models
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		set.Profiles[i].Config = cfg
		set.ActiveID = profileID
		return store.SaveProfiles(set)
	}
	return fmt.Errorf("找不到配置 %s", profileID)
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
