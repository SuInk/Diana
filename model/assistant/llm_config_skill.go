package assistant

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

type llmConfigCommand struct {
	Matched     bool
	Provider    llm.Provider
	ProviderSet bool
	Model       string
	Err         error
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

// handleLLMConfigRequest 处理机器人内置的聊天 LLM 配置修改请求。
func handleLLMConfigRequest(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	// 这里先做自然语言意图抽取，只有明确要改 provider/model 时才接管消息。
	command := parseLLMConfigIntent(req.Text)
	if !command.Matched {
		return nil, nil
	}
	if ownerID := strings.TrimSpace(req.OwnerID); ownerID == "" {
		reply := "未配置主人 QQ，无法通过聊天修改 LLM 配置。"
		recordLLMConfigCommandLog(ctx, req, llmConfigApplyResult{Reply: reply}, nil)
		return &PluginResponse{Handled: true, Reply: reply}, nil
	} else if req.Event.UserID != ownerID {
		reply := "只有主人可以修改 LLM 配置。"
		recordLLMConfigCommandLog(ctx, req, llmConfigApplyResult{Reply: reply}, nil)
		return &PluginResponse{Handled: true, Reply: reply}, nil
	}
	if command.Err != nil {
		reply := command.Err.Error() + "\n" + llmConfigUsage()
		recordLLMConfigCommandLog(ctx, req, llmConfigApplyResult{Reply: reply}, command.Err)
		return &PluginResponse{Handled: true, Reply: reply}, nil
	}
	if req.LLMStore == nil {
		reply := "当前未接入 LLM 配置集。"
		recordLLMConfigCommandLog(ctx, req, llmConfigApplyResult{Reply: reply}, nil)
		return &PluginResponse{Handled: true, Reply: reply}, nil
	}

	result := applyLLMConfigCommand(ctx, req.LLMStore, command, req.LLMModelLister)
	recordLLMConfigCommandLog(ctx, req, result, nil)
	return &PluginResponse{Handled: true, Reply: result.Reply}, nil
}

// applyLLMConfigCommand 将自然语言配置意图应用到当前 LLM profile。
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
	if err := ensureLLMModelAvailable(ctx, nextCfg, listModels); err != nil {
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
		store.SaveProfiles(set)
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

// recordLLMConfigCommandLog 记录聊天修改 LLM 配置的审计日志。
func recordLLMConfigCommandLog(ctx context.Context, req PluginRequest, result llmConfigApplyResult, err error) {
	if req.AppLogs == nil {
		return
	}
	// 聊天修改 LLM 配置会影响运行时行为，所以和 WebUI 配置变更写入同一条审计流。
	// 成功算操作日志，被拒绝或失败算错误日志，操作者记录为 QQ 用户。
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
		Actor:    qqEventActor(req.Event),
		Target:   firstNonEmpty(result.ProfileID, result.NewModel, result.OldModel),
		Metadata: metadata,
	})
}

// qqEventActor 将 QQ 事件转换为日志操作者标识。
func qqEventActor(event MessageEvent) string {
	// 给 actor 加命名空间，日志中心里能区分 WebUI 操作者和 QQ 用户。
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
func ensureLLMModelAvailable(ctx context.Context, cfg llm.ProviderConfig, listModels LLMModelLister) error {
	model := strings.TrimSpace(cfg.Model)
	// listModels 会走当前 provider 的真实后端接口；不能靠本地硬编码模型名判断。
	models, err := listModels(ctx, cfg)
	if err != nil {
		return fmt.Errorf("无法读取 %s 的模型列表，未保存；请先在 WebUI 的模型列表里选择可用模型。%v", cfg.Provider, err)
	}
	if modelInList(model, models) {
		return nil
	}
	return fmt.Errorf("模型 %s 不在 %s 的模型列表中，未保存。可选：%s", model, cfg.Provider, summarizeModelIDs(models))
}

// modelInList 判断模型名是否存在于模型列表中。
func modelInList(model string, models []llm.ModelInfo) bool {
	model = strings.TrimSpace(model)
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate.ID), model) {
			return true
		}
	}
	return false
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

// parseLLMConfigIntent 从自然语言中提取 LLM 配置修改意图。
func parseLLMConfigIntent(text string) llmConfigCommand {
	command := strings.TrimSpace(text)
	if command == "" {
		return llmConfigCommand{}
	}
	provider, providerSet := extractLLMProvider(command)
	model := extractLLMModel(command)
	if !providerSet && model == "" {
		return llmConfigCommand{}
	}
	// provider/model 名只是候选，必须同时出现“切换/改用/以后用”等变更意图才会执行。
	if !hasLLMConfigChangeIntent(command) {
		return llmConfigCommand{}
	}
	if !providerSet {
		if inferred, ok := inferProviderFromModel(model); ok {
			provider = inferred
			providerSet = true
		}
	}
	return llmConfigCommand{
		Matched:     true,
		Provider:    provider,
		ProviderSet: providerSet,
		Model:       model,
	}
}

// hasLLMConfigChangeIntent 判断文本是否包含配置变更意图。
func hasLLMConfigChangeIntent(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	strongPhrases := []string{
		"切换", "切到", "切成", "换成", "换到", "换用", "改成", "改为", "改到", "改用",
		"设为", "设成", "设置为", "设置成", "调整为", "更新为", "指定为",
		"switch to", "change to", "set to", "use provider", "use model",
	}
	for _, phrase := range strongPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	targetMentioned := strings.Contains(normalized, "llm") ||
		strings.Contains(normalized, "provider") ||
		strings.Contains(normalized, "model") ||
		strings.Contains(normalized, "模型") ||
		strings.Contains(normalized, "提供商") ||
		strings.Contains(normalized, "供应商")
	if targetMentioned && strings.ContainsAny(normalized, "切换改设用调更换") {
		return true
	}
	naturalUsePhrases := []string{"以后用", "之后用", "后面用", "接下来用", "现在用", "当前用", "默认用", "改用", "换用"}
	for _, phrase := range naturalUsePhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// extractLLMProvider 从文本中识别 provider 别名。
func extractLLMProvider(text string) (llm.Provider, bool) {
	type providerAlias struct {
		Provider llm.Provider
		Terms    []string
	}
	aliases := []providerAlias{
		{Provider: llm.ProviderOpenAICompatible, Terms: []string{"openai compatible", "openai-compatible", "openai_compatible", "openai兼容", "openai"}},
		{Provider: llm.ProviderGemini, Terms: []string{"google genai", "google-genai", "google_genai", "gemini", "谷歌"}},
		{Provider: llm.ProviderAnthropic, Terms: []string{"anthropic", "claude官方", "claude"}},
	}
	lower := strings.ToLower(text)
	for _, alias := range aliases {
		for _, term := range alias.Terms {
			if containsAlias(lower, term) {
				return alias.Provider, true
			}
		}
	}
	return "", false
}

// containsAlias 判断文本是否包含指定 provider 别名。
func containsAlias(text string, alias string) bool {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return false
	}
	if containsChinese(alias) {
		return strings.Contains(text, alias)
	}
	// 英文 provider 别名要按单词边界匹配，避免 openai 误命中更长的无关字符串。
	pattern := `(^|[^a-z0-9])` + regexp.QuoteMeta(alias) + `([^a-z0-9]|$)`
	return regexp.MustCompile(pattern).FindStringIndex(text) != nil
}

// containsChinese 判断字符串是否包含中文字符。
func containsChinese(text string) bool {
	for _, r := range text {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}

var llmModelTokenPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9._:/-]*[A-Za-z0-9]`)

// extractLLMModel 从文本中提取候选模型名。
func extractLLMModel(text string) string {
	matches := llmModelTokenPattern.FindAllString(text, -1)
	// 自然语言里模型名通常出现在句尾，从后往前找能减少误把 provider 当 model 的概率。
	for i := len(matches) - 1; i >= 0; i-- {
		candidate := strings.Trim(matches[i], " \t\r\n，。！？、；;：:,.)]}")
		if looksLikeLLMModel(candidate) {
			return candidate
		}
	}
	return ""
}

// looksLikeLLMModel 判断候选 token 是否像模型名。
func looksLikeLLMModel(candidate string) bool {
	if candidate == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(candidate))
	switch normalized {
	case "llm", "model", "provider", "openai", "gemini", "anthropic", "claude", "google", "genai":
		return false
	}
	if strings.ContainsAny(normalized, ".-/_:") || hasDigit(normalized) {
		return true
	}
	for _, prefix := range []string{
		"gpt", "gp", "o", "deepseek", "qwen", "moonshot", "kimi", "glm", "doubao", "ernie",
		"hunyuan", "yi", "claude", "gemini", "llama", "mistral", "mixtral", "command",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// hasDigit 判断字符串中是否包含数字。
func hasDigit(text string) bool {
	for _, r := range text {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// inferProviderFromModel 根据模型名前缀推断 provider。
func inferProviderFromModel(model string) (llm.Provider, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gemini"):
		return llm.ProviderGemini, true
	case strings.HasPrefix(normalized, "claude"):
		return llm.ProviderAnthropic, true
	case strings.HasPrefix(normalized, "gpt") || strings.HasPrefix(normalized, "gp") || regexp.MustCompile(`^o[0-9]`).MatchString(normalized):
		return llm.ProviderOpenAICompatible, true
	default:
		return "", false
	}
}

// llmConfigUsage 返回聊天修改 LLM 配置的用法提示。
func llmConfigUsage() string {
	return "可以直接说：把提供商切到 gemini、把模型换成 gemini-2.5-pro、以后用 anthropic 的 claude-sonnet-4-5。模型必须存在于当前 provider 的模型列表里。支持 provider：openai_compatible、gemini、anthropic"
}
