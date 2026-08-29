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
		Name:        "提供商配置",
		Version:     "0.1.0",
		Description: "官方内置提供商配置能力；自然语言由主 Agent 理解，配置修改仅通过主人专属结构化工具执行。",
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
	// Role 是要改的用途：chat / vision / intent / image。空表示对话。
	Role string
}

type llmConfigApplyResult struct {
	Reply       string
	Role        string
	Updated     bool
	ProfileID   string
	ProfileName string
	OldProvider llm.Provider
	NewProvider llm.Provider
	OldModel    string
	NewModel    string
}

// applyLLMConfigCommand 把「换个模型」落到这台机器人的模型分配上。
//
// 改的是 BotConfig.ModelRoles，不是提供商配置本身。两者管的是不同的事：provider
// 的地址、密钥、默认模型属于「这个 provider 怎么连」，归 WebUI 的「提供商」页；
// 「这台机器人各个用途分别用哪个模型」属于机器人配置，也就是模型分配。
//
// 四个用途都能改：对话、视觉理解、意图识别、图片生成，由 command.Role 指定，
// 默认对话。它们和 WebUI「模型分配」那四行是同一份数据。
//
// 以前这里改的是激活配置的默认模型。只要配过模型分配，roleBoundProfiles 就会用
// role.Model 覆盖它——回执说「已更新 Model：old -> new」，对话实际还在用旧模型。
// 一个会说谎的成功回执比失败更难查。
func (r *Runtime) applyLLMConfigCommand(ctx context.Context, command llmConfigCommand, listModels LLMModelLister) llmConfigApplyResult {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return llmConfigApplyResult{Reply: "当前未接入提供商配置集。"}
	}
	if listModels == nil {
		listModels = defaultLLMModelLister
	}
	set := store.Profiles().WithDefaults()
	botCfg := r.Config().WithDefaults()
	roles := normalizeModelRoles(botCfg.ModelRoles)
	roleKey, ok := normalizeLLMConfigRole(command.Role)
	if !ok {
		return llmConfigApplyResult{Reply: "更新失败：不认识的用途 " + command.Role + "，只能是 chat、vision、intent、image。"}
	}

	boundProfile, boundModel, ok := modelRoleBinding(set, roles, roleKey)
	if !ok {
		return llmConfigApplyResult{Reply: "当前没有可用的提供商配置。"}
	}
	oldProvider := boundProfile.Config.Provider
	oldModel := boundModel

	target, model, err := resolveLLMConfigTarget(set, boundProfile, boundModel, command)
	if err != nil {
		return llmConfigApplyResult{Reply: "更新失败：" + err.Error(), Role: roleKey, OldProvider: oldProvider, OldModel: oldModel}
	}

	probe := target.Config.WithDefaults()
	probe.Model = model
	// 必须先问后端模型列表，防止切到 provider 里不存在的模型后机器人直接不可用。
	modelInfo, err := ensureLLMModelAvailable(ctx, probe, listModels)
	if err != nil {
		return llmConfigApplyResult{
			Reply:       "更新失败：" + err.Error(),
			Role:        roleKey,
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
		Role:        roleKey,
		ProfileID:   target.ID,
		ProfileName: target.Name,
		OldProvider: oldProvider,
		NewProvider: probe.Provider,
		OldModel:    oldModel,
		NewModel:    model,
	}
	if err := r.saveModelRole(botCfg, roles, roleKey, target, model); err != nil {
		return llmConfigApplyResult{Reply: "更新失败：机器人配置没能保存（" + err.Error() + "）。"}
	}
	result.Reply = fmt.Sprintf("已把%s模型换成 %s（配置：%s）。改的是机器人模型分配里的这一档，没有动提供商配置里的 provider 设置，其余用途各自的分配保持不变。%s%s",
		llmConfigRoleLabel(roleKey), model, target.Name, llmConfigFollowChatNote(roleKey, roles), notes)
	return result
}

// llmConfigFollowChatNote 在第一次给对话定下绑定时说明连带影响：视觉理解、意图
// 识别、图片生成没有单独分配时跟随对话（这也是 WebUI 模型分配页写明的规则），
// 所以这一次改动会连它们一起改掉。不说的话就是一次静默的连带变更。
func llmConfigFollowChatNote(roleKey string, previous map[string]ModelRole) string {
	if roleKey != llmConfigRoleChat {
		return ""
	}
	unassigned := make([]string, 0, 3)
	for _, key := range []string{llmConfigRoleVision, llmConfigRoleIntent, llmConfigRoleImage} {
		if _, ok := previous[key]; !ok {
			unassigned = append(unassigned, llmConfigRoleLabel(key))
		}
	}
	if len(unassigned) == 0 {
		return ""
	}
	return strings.Join(unassigned, "、") + "没有单独分配，会跟随对话模型。"
}

// 模型分配的四个用途。和 WebUI「模型分配」那四行、normalizeModelRoles 的白名单
// 是同一套 key。
const (
	llmConfigRoleChat   = "chat"
	llmConfigRoleVision = "vision"
	llmConfigRoleIntent = "intent"
	llmConfigRoleImage  = "image"
)

func normalizeLLMConfigRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", llmConfigRoleChat, "default", "text":
		return llmConfigRoleChat, true
	case llmConfigRoleVision:
		return llmConfigRoleVision, true
	case llmConfigRoleIntent:
		return llmConfigRoleIntent, true
	case llmConfigRoleImage:
		return llmConfigRoleImage, true
	default:
		return "", false
	}
}

func llmConfigRoleLabel(role string) string {
	switch role {
	case llmConfigRoleVision:
		return "视觉理解"
	case llmConfigRoleIntent:
		return "意图识别"
	case llmConfigRoleImage:
		return "图片生成"
	default:
		return "对话"
	}
}

// modelRoleBinding 报出「某个用途现在实际用哪套配置的哪个模型」，用于回执里的
// 「旧模型」。回落顺序和运行时一致：本用途绑定 -> chat 绑定 -> 该用途的配置分组
// -> 激活配置。图片生成那一档取的是生图模型，不是对话模型。
func modelRoleBinding(set llm.ProfileSet, roles map[string]ModelRole, roleKey string) (llm.Profile, string, bool) {
	if profile, model, ok := profilesForRole(set, roles, roleKey); ok {
		return profile, model, true
	}
	if roleKey != llmConfigRoleChat {
		if profile, model, ok := profilesForRole(set, roles, llmConfigRoleChat); ok {
			return profile, model, true
		}
		if candidates := set.GroupProfiles(roleKey); len(candidates) > 0 {
			return candidates[0], roleModelFromProfile(candidates[0], roleKey), true
		}
	}
	current, ok := set.FirstProfile()
	if !ok {
		return llm.Profile{}, "", false
	}
	return current, roleModelFromProfile(current, roleKey), true
}

// profilesForRole 解析一条已存在的绑定。
func profilesForRole(set llm.ProfileSet, roles map[string]ModelRole, roleKey string) (llm.Profile, string, bool) {
	role, ok := roles[roleKey]
	if !ok {
		return llm.Profile{}, "", false
	}
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
	if len(candidates) == 0 {
		return llm.Profile{}, "", false
	}
	return candidates[0], role.Model, true
}

func roleModelFromProfile(profile llm.Profile, roleKey string) string {
	cfg := profile.Config.WithDefaults()
	if roleKey == llmConfigRoleImage {
		return cfg.ImageModelWithDefault()
	}
	return cfg.Model
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
	return fmt.Sprintf("提醒：这套配置的最大输出 Token 填的是 %d，超过新模型的 %d，需要在 WebUI 的提供商配置里调低。", cfg.MaxOutputTokens, info.MaxOutputTokens)
}

// saveModelRole 只改指定用途的绑定，其余用途原样保留。
func (r *Runtime) saveModelRole(botCfg BotConfig, roles map[string]ModelRole, roleKey string, target llm.Profile, model string) error {
	next := make(map[string]ModelRole, len(roles)+1)
	for key, role := range roles {
		next[key] = role
	}
	role := next[roleKey]
	// 原来按分组绑定、而新配置仍在那个分组里时保留分组绑定，只换模型：
	// 分组绑定带故障转移，改成单配置会把这个能力弄丢。
	if role.Group != "" && profileInGroup(target, role.Group) {
		role.Model = model
	} else {
		role = ModelRole{ProfileID: target.ID, Model: model}
	}
	next[roleKey] = role
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

// recordLLMConfigSkillLog 记录聊天修改提供商配置的审计日志。
func recordLLMConfigSkillLog(ctx context.Context, req PluginRequest, result llmConfigApplyResult, err error) {
	if req.AppLogs == nil {
		return
	}
	// 聊天修改提供商配置会影响运行时行为，所以和 WebUI 配置变更写入同一条审计流。
	// 成功算操作日志，被拒绝或失败算错误日志，操作者记录为用户。
	kind := applog.KindError
	level := applog.LevelError
	message := result.Reply
	if result.Updated {
		kind = applog.KindOperation
		level = applog.LevelInfo
		message = "聊天修改提供商配置成功"
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
	if result.Role != "" {
		metadata["role"] = result.Role
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

// ensureLLMModelAvailable 校验模型确实在这个 provider 的模型清单里。
// 只认后端返回的清单：本地硬编码的模型名判断不了中转和自建服务。
func ensureLLMModelAvailable(ctx context.Context, cfg llm.ProviderConfig, listModels LLMModelLister) (llm.ModelInfo, error) {
	model := strings.TrimSpace(cfg.Model)
	// listModels 会走当前 provider 的真实后端接口；不能靠本地硬编码模型名判断。
	models, err := listModels(ctx, cfg)
	if err != nil {
		return llm.ModelInfo{}, fmt.Errorf("无法读取 %s 的模型列表，未保存；请先在 WebUI 的模型列表里选择可用模型。%v", cfg.Provider, err)
	}
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate.ID), model) {
			return candidate, nil
		}
	}
	return llm.ModelInfo{}, fmt.Errorf("模型 %s 不在 %s 的模型列表中，未保存。可选：%s", model, cfg.Provider, summarizeModelIDs(models))
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
