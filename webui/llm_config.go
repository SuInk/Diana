// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/llmauth"

	"github.com/gin-gonic/gin"
)

type LLMConfigHandler struct {
	store      LLMProfileStore
	newClient  LLMClientFactory
	listModels LLMModelListFactory
	logs       AppLogWriter
	// botProfiles 用来回答「这套配置正被哪个机器人的哪个用途、按哪个模型使用」。
	// 没有它时这一页只能按配置自己的默认模型说话，而机器人多半用的是模型分配里
	// 另选的模型——同一个页面上写着一个不生效的窗口，比不写更误导。
	botProfiles BotModelRoleSource
	// oauth 为空表示这套部署没启用 OAuth 登录（没有持久化存储时就是如此）。
	oauth *llmauth.Manager
}

// BotModelRoleSource 提供各机器人的模型分配。
type BotModelRoleSource interface {
	Profiles() assistant.ProfileSet
}

// SetBotProfileSource 注入机器人配置集，用于展示模型分配对这套提供商配置的引用。
func (h *LLMConfigHandler) SetBotProfileSource(source BotModelRoleSource) {
	h.botProfiles = source
}

type LLMClientFactory func(llm.ProviderConfig) (llm.LLMClient, error)
type LLMModelListFactory func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error)

type llmConfigPayload struct {
	ID               string             `json:"id,omitempty"`
	Name             string             `json:"name,omitempty"`
	Group            string             `json:"group,omitempty"`
	Description      string             `json:"description,omitempty"`
	UpdatedAt        string             `json:"updated_at,omitempty"`
	Profiles         []llmConfigPayload `json:"profiles,omitempty"`
	Provider         llm.Provider       `json:"provider"`
	APIStyle         llm.APIStyle       `json:"api_style,omitempty"`
	APIFormat        llm.APIFormat      `json:"api_format,omitempty"`
	APIKey           string             `json:"api_key,omitempty"`
	APIKeyConfigured bool               `json:"api_key_configured,omitempty"`
	APIKeyPreview    string             `json:"api_key_preview,omitempty"`
	BaseURL          string             `json:"base_url,omitempty"`
	Models           []llm.ModelInfo    `json:"models,omitempty"`
	Model            string             `json:"model"`
	ImageModel       string             `json:"image_model,omitempty"`
	ImageBaseURL     string             `json:"image_base_url,omitempty"`
	ImageOrigin      string             `json:"image_origin,omitempty"`
	ImageTimeoutMS   int64              `json:"image_timeout_ms,omitempty"`
	UserAgent        string             `json:"user_agent,omitempty"`
	Headers          map[string]string  `json:"headers,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	ReasoningEffort  string             `json:"reasoning_effort,omitempty"`
	// 这两个是「用户手填的覆盖值」，不是当前生效值：指针类型才分得清「没提交这个
	// 字段」（nil，保留旧值）和「清空了这个输入框」（0，改回自动）。以前它们是 int64，
	// 清空输入框和没提交长得一样，于是填过的值永远删不掉。
	ContextWindowTokens *int64 `json:"context_window_tokens"`
	MaxContextTokens    *int64 `json:"max_context_tokens"`
	// 下面两个是只读回显：当前这个模型实际生效的窗口和请求上限，以及窗口的来源。
	// 界面据此提示「未填写 · 当前按模型清单为 1,050,000」，而不是把推断值预填进
	// 输入框冒充用户设置——那正是「模型默认填了 400k」的由来。
	// RoleBindings 列出机器人模型分配里指向这套配置的用途，用来说明「改这套配置
	// 会影响谁」。
	RoleBindings                 []llmRoleBinding        `json:"role_bindings,omitempty"`
	EffectiveContextWindowTokens int64                   `json:"effective_context_window_tokens,omitempty"`
	EffectiveMaxContextTokens    int64                   `json:"effective_max_context_tokens,omitempty"`
	ContextWindowSource          llm.ContextWindowSource `json:"context_window_source,omitempty"`
	// CatalogContextWindowTokens 是同步下来的模型清单里记的窗口，只作参考值展示：
	// 界面用它提示「这个模型写着多少，可以照着填」，它不参与任何计算。
	CatalogContextWindowTokens int64 `json:"catalog_context_window_tokens,omitempty"`
	// MaxOutputTokens 和窗口一样用指针：nil 是「这个客户端没提交」，保留旧值；提交 0
	// 是明确改回「按模型上限」。以前是普通整数，界面不提交它，一保存就被清成 0。
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	// EffectiveMaxOutputTokens 和 MaxOutputTokensSource 是只读回显：默认模型在调用方
	// 没覆盖时实际发出的输出上限和它的来源；0 表示不发，由服务端按模型处理。
	EffectiveMaxOutputTokens int64                     `json:"effective_max_output_tokens,omitempty"`
	MaxOutputTokensSource    llm.MaxOutputTokensSource `json:"max_output_tokens_source,omitempty"`
	TimeoutMS                int64                     `json:"timeout_ms,omitempty"`
}

// llmRoleBinding 是「某个机器人的某个用途绑到了这套配置的哪个模型」。
//
// 不带窗口：上下文窗口是配置级的设置，一套配置一个值，用途换模型也不跟着变。
// 这里只回答「谁在用这套配置、用的哪个模型」。
type llmRoleBinding struct {
	BotID     string `json:"bot_id,omitempty"`
	BotName   string `json:"bot_name,omitempty"`
	Role      string `json:"role"`
	RoleLabel string `json:"role_label"`
	Model     string `json:"model"`
}

type llmTestPayload struct {
	Message string `json:"message"`
	Mode    string `json:"mode,omitempty"`
}

type llmModelsPayload struct {
	Models []llm.ModelInfo `json:"models"`
}

const minLLMAPIKeyChars = 8

var llmModelListTimeout = 8 * time.Second

// NewLLMConfigHandler 创建 LLMConfigHandler 实例。
//
// 默认的客户端与模型列表工厂都在调用时才去看 OAuth 管理器：这一页上的「测试」
// 和「拉取模型」必须和机器人真正发请求时用同一套凭据，否则绑了 OAuth 的配置档
// 会在这里以「没有 API Key」失败，而实际运行是好的。
func NewLLMConfigHandler(store LLMProfileStore) *LLMConfigHandler {
	handler := &LLMConfigHandler{store: store}
	handler.newClient = func(cfg llm.ProviderConfig) (llm.LLMClient, error) {
		return llm.NewClient(cfg, handler.clientOptions(cfg)...)
	}
	handler.listModels = func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return llm.ListModels(ctx, cfg, handler.clientOptions(cfg)...)
	}
	return handler
}

// clientOptions 按配置档补上 OAuth 凭据；没绑或没启用 OAuth 时返回空。
func (h *LLMConfigHandler) clientOptions(cfg llm.ProviderConfig) []llm.ClientOption {
	if h == nil || h.oauth == nil {
		return nil
	}
	return llm.ClientOptionsFor(cfg, h.oauth)
}

// NewLLMConfigHandlerWithFactory 创建 LLMConfigHandler 实例。
func NewLLMConfigHandlerWithFactory(store LLMProfileStore, factory LLMClientFactory) *LLMConfigHandler {
	handler := NewLLMConfigHandler(store)
	handler.newClient = factory
	return handler
}

// SetModelListFactory 注入模型列表读取实现。
func (h *LLMConfigHandler) SetModelListFactory(factory LLMModelListFactory) {
	h.listModels = factory
}

// SetLogStore 注入提供商配置接口的日志写入器。
func (h *LLMConfigHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册提供商配置、配置集、模型列表和测试接口。
func (h *LLMConfigHandler) Register(router gin.IRouter) {
	router.GET("/api/llm/config", h.getConfig)
	router.GET("/api/llm/config/export", h.exportConfig)
	router.POST("/api/llm/config", h.saveConfig)
	router.POST("/api/llm/config/clone", h.cloneProfile)
	router.POST("/api/llm/config/delete", h.deleteProfile)
	router.POST("/api/llm/config/import", h.importProfiles)
	router.POST("/api/llm/config/reorder", h.reorderProfiles)
	router.GET("/api/llm/models", h.models)
	router.POST("/api/llm/models", h.models)
	router.POST("/api/llm/test", h.test)
	router.POST("/api/llm/persona", h.personaGenerate)
	router.POST("/api/llm/persona/lint", h.personaLintReview)
	router.GET("/api/llm/providers", h.providers)
	router.POST("/api/llm/providers/models", h.providerModels)
	router.POST("/api/llm/providers/test", h.providerTest)
	h.registerOAuthRoutes(router)
}

// providers exposes the provider/model view used by the new management UI.
// It is derived from legacy profiles during the compatibility period and never
// serializes API keys.
func (h *LLMConfigHandler) providers(c *gin.Context) {
	registry, _, err := llm.NewProviderRegistryFromProfiles(h.store.Profiles())
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm_providers", err, "", nil)
		return
	}
	providers := make([]llm.ProviderDefinition, 0)
	for _, profile := range h.store.Profiles().Profiles {
		if provider, ok := registry.PublicProvider(profile.ID); ok {
			providers = append(providers, provider)
		}
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers, "models": registry.Models()})
}

type providerSelectionPayload struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId,omitempty"`
	Message    string `json:"message,omitempty"`
}

func (h *LLMConfigHandler) providerModels(c *gin.Context) {
	var payload providerSelectionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_providers_models", err, "", nil)
		return
	}
	registry, _, err := llm.NewProviderRegistryFromProfiles(h.store.Profiles())
	if err != nil {
		h.writeError(c, 422, "llm_providers_models", err, payload.ProviderID, nil)
		return
	}
	models, err := registry.ListModels(c.Request.Context(), payload.ProviderID)
	if err != nil {
		h.writeError(c, 502, "llm_providers_models", err, payload.ProviderID, nil)
		return
	}
	c.JSON(http.StatusOK, llmModelsPayload{Models: models})
}

func (h *LLMConfigHandler) providerTest(c *gin.Context) {
	var payload providerSelectionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_providers_test", err, "", nil)
		return
	}
	registry, _, err := llm.NewProviderRegistryFromProfiles(h.store.Profiles())
	if err != nil {
		h.writeError(c, 422, "llm_providers_test", err, payload.ProviderID, nil)
		return
	}
	if strings.TrimSpace(payload.Message) == "" {
		payload.Message = "ping"
	}
	started := time.Now()
	response, err := registry.Generate(c.Request.Context(), llm.AgentModelConfig{ProviderID: payload.ProviderID, ModelID: payload.ModelID}, llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: payload.Message}}})
	if err == nil {
		recordLLMUsage(c, h.logs, response.Provider, firstNonEmpty(response.Model, payload.ModelID), response.Usage, "webui_provider_test", time.Since(started))
	}
	if err != nil {
		publicErr := h.providerTestError(payload.ProviderID, err)
		metadata := map[string]any{
			"provider_id": strings.TrimSpace(payload.ProviderID),
			"model_id":    strings.TrimSpace(payload.ModelID),
		}
		if provider, ok := registry.Provider(payload.ProviderID); ok {
			metadata["protocol"] = provider.Protocol
			if provider.BaseURL != "" {
				metadata["base_url"] = provider.BaseURL
			}
		}
		log.Printf("llm provider test failed: provider=%q model=%q err=%v", payload.ProviderID, payload.ModelID, publicErr)
		h.writeError(c, 502, "llm_providers_test", publicErr, payload.ProviderID, metadata)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *LLMConfigHandler) providerTestError(providerID string, err error) error {
	message := ""
	if err != nil {
		message = err.Error()
	}
	for _, profile := range h.store.Profiles().Profiles {
		if strings.TrimSpace(profile.ID) != strings.TrimSpace(providerID) {
			continue
		}
		if secret := strings.TrimSpace(profile.Config.APIKey); secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
		break
	}
	if strings.TrimSpace(message) == "" {
		message = "上游没有返回错误详情"
	}
	return fmt.Errorf("提供商测试失败：%s", message)
}

// getConfig 处理提供商配置读取请求。
func (h *LLMConfigHandler) getConfig(c *gin.Context) {
	// 默认响应不带 API Key；本地配置页需要编辑时显式带 include_secrets=true。
	if queryBool(c.Query("include_secrets")) {
		c.JSON(200, payloadFromProfileSetWithSecrets(h.store.Profiles()))
		return
	}
	c.JSON(200, h.profileSetPayload(h.store.Profiles()))
}

// exportConfig 导出包含密钥的提供商配置集。
func (h *LLMConfigHandler) exportConfig(c *gin.Context) {
	c.JSON(200, payloadFromProfileSetWithSecrets(h.store.Profiles()))
}

// saveConfig 保存当前提供商配置或新增配置档。
func (h *LLMConfigHandler) saveConfig(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_config_save", err, "", nil)
		return
	}

	set := h.store.Profiles()
	cfg := configFromPayload(payload)
	existing := existingProfileConfig(set, payload)
	cfg = mergeUnsubmittedLLMConfig(payload, cfg, existing)
	// 前端留空 API Key 表示沿用已保存密钥，不表示把密钥清空。
	if cfg.APIKey == "" && existing.Provider == cfg.Provider {
		cfg.APIKey = existing.APIKey
	}
	// 旧版前端不会提交 models；编辑同一渠道时保留已缓存的完整模型列表。
	// 地址改变时不沿用，避免把旧 Provider 的模型错误展示到新服务。
	if payload.Models == nil && existing.Provider == cfg.Provider && strings.TrimSpace(existing.BaseURL) == strings.TrimSpace(cfg.BaseURL) {
		cfg.Models = existing.Models
	}
	if strings.TrimSpace(payload.APIKey) != "" && utf8.RuneCountInString(cfg.APIKey) < minLLMAPIKeyChars {
		h.writeError(c, 400, "llm_config_save", fmt.Errorf("api_key must be at least %d characters", minLLMAPIKeyChars), llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
		return
	}
	if err := cfg.ValidateChannel(); err != nil {
		h.writeError(c, 400, "llm_config_save", err, llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
		return
	}
	if strings.TrimSpace(cfg.Model) == "" {
		if len(cfg.Models) == 0 {
			models, err := h.listModels(c.Request.Context(), cfg)
			if err != nil {
				h.writeError(c, 502, "llm_config_save_models", err, llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
				return
			}
			cfg.Models = models
		}
		if len(cfg.Models) == 0 || strings.TrimSpace(cfg.Models[0].ID) == "" {
			h.writeError(c, 422, "llm_config_save_models", fmt.Errorf("provider returned no usable models"), llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
			return
		}
		cfg.Model = strings.TrimSpace(cfg.Models[0].ID)
	}

	next := upsertProfileSet(set, payload, cfg)
	// 落库失败就不能回 200，否则前端提示保存成功、重启后配置又变回旧值。
	if err := h.store.SaveProfiles(next); err != nil {
		h.writeError(c, 500, "llm_config_save", err, payload.ID, llmLogMetadata(cfg, payload.ID))
		return
	}
	recordRequestOperation(c, h.logs, "llm_config_save", "提供商配置已保存", payload.ID, llmLogMetadata(cfg, payload.ID))
	c.JSON(200, h.profileSetPayload(next))
}

// reorderProfiles 按给定 ID 顺序重排配置档；组内顺序即失败降级的优先级。
func (h *LLMConfigHandler) reorderProfiles(c *gin.Context) {
	var payload struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_profile_reorder", err, "", nil)
		return
	}
	if len(payload.IDs) == 0 {
		h.writeError(c, 400, "llm_profile_reorder", fmt.Errorf("ids is required"), "", nil)
		return
	}
	set := h.store.Profiles().Reorder(payload.IDs)
	if err := h.store.SaveProfiles(set); err != nil {
		h.writeError(c, 500, "llm_profile_reorder", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "llm_profile_reorder", "提供商配置优先级已调整", "", nil)
	c.JSON(200, h.profileSetPayload(set))
}

// deleteProfile 删除指定提供商配置档。
func (h *LLMConfigHandler) deleteProfile(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_profile_delete", err, "", nil)
		return
	}
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		h.writeError(c, 400, "llm_profile_delete", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	set := h.store.Profiles()
	if len(set.Profiles) <= 1 {
		h.writeError(c, 400, "llm_profile_delete", fmt.Errorf("at least one llm profile must remain"), targetID, nil)
		return
	}
	next := set.Delete(targetID)
	if len(next.Profiles) == len(set.Profiles) {
		h.writeError(c, 404, "llm_profile_delete", fmt.Errorf("profile %q not found", targetID), targetID, nil)
		return
	}
	if err := h.store.SaveProfiles(next); err != nil {
		h.writeError(c, 500, "llm_profile_delete", err, targetID, map[string]any{"profile_id": targetID})
		return
	}
	recordRequestOperation(c, h.logs, "llm_profile_delete", "提供商配置已删除", targetID, map[string]any{"profile_id": targetID})
	c.JSON(200, h.profileSetPayload(next))
}

// cloneProfile 复制指定提供商配置档。
func (h *LLMConfigHandler) cloneProfile(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_profile_clone", err, "", nil)
		return
	}
	sourceID := strings.TrimSpace(payload.ID)
	if sourceID == "" {
		if first, ok := h.store.Profiles().FirstProfile(); ok {
			sourceID = first.ID
		}
	}
	set := h.store.Profiles()
	for _, profile := range set.Profiles {
		if profile.ID != sourceID {
			continue
		}
		cloned := payloadFromConfig(profile.Config)
		cloned.Name = profile.Name + " 副本"
		cloned.Group = profile.Group
		cloned.Description = profile.Description
		next := upsertProfileSet(set, llmConfigPayload{Name: cloned.Name, Group: cloned.Group, Description: cloned.Description}, profile.Config)
		if err := h.store.SaveProfiles(next); err != nil {
			h.writeError(c, 500, "llm_profile_clone", err, sourceID, llmLogMetadata(profile.Config, sourceID))
			return
		}
		recordRequestOperation(c, h.logs, "llm_profile_clone", "提供商配置已复制", sourceID, llmLogMetadata(profile.Config, sourceID))
		c.JSON(200, h.profileSetPayload(next))
		return
	}
	h.writeError(c, 404, "llm_profile_clone", fmt.Errorf("profile %q not found", sourceID), sourceID, nil)
}

// importProfiles 导入一组提供商配置档。
func (h *LLMConfigHandler) importProfiles(c *gin.Context) {
	var payload struct {
		// 导出的旧文件里可能还带着 active_profile_id，解码时直接忽略即可。
		Profiles []llmConfigPayload `json:"profiles"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_profile_import", err, "", nil)
		return
	}
	if len(payload.Profiles) == 0 {
		h.writeError(c, 400, "llm_profile_import", fmt.Errorf("profiles are required"), "", nil)
		return
	}
	next := llm.ProfileSet{Profiles: make([]llm.Profile, 0, len(payload.Profiles))}
	seenIDs := make(map[string]struct{}, len(payload.Profiles))
	for _, item := range payload.Profiles {
		// 导入文件必须自带密钥，避免导入后看似成功但实际无法调用模型。
		cfg := configFromPayload(item)
		if cfg.APIKey == "" {
			h.writeError(c, 400, "llm_profile_import", fmt.Errorf("profile %q missing api_key", firstNonEmpty(item.Name, item.ID)), firstNonEmpty(item.ID, item.Name), nil)
			return
		}
		if err := cfg.ValidateChannel(); err != nil {
			h.writeError(c, 400, "llm_profile_import", err, firstNonEmpty(item.ID, item.Name), llmLogMetadata(cfg, item.ID))
			return
		}
		id := firstNonEmpty(strings.TrimSpace(item.ID), llm.NewProfileSet(cfg).Profiles[0].ID)
		if _, ok := seenIDs[id]; ok {
			h.writeError(c, 400, "llm_profile_import", fmt.Errorf("duplicate profile id %q", id), id, nil)
			return
		}
		seenIDs[id] = struct{}{}
		updatedAt := time.Now()
		if item.UpdatedAt != "" {
			if parsed, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
				updatedAt = parsed
			}
		}
		next.Profiles = append(next.Profiles, llm.Profile{
			ID:          id,
			Name:        llm.NormalizeProfileName(item.Name),
			Group:       llm.NormalizeProfileGroup(item.Group),
			Description: strings.TrimSpace(item.Description),
			UpdatedAt:   updatedAt,
			Config:      cfg,
		})
	}
	if err := h.store.SaveProfiles(next); err != nil {
		h.writeError(c, 500, "llm_profile_import", err, next.Profiles[0].ID, map[string]any{"profile_count": len(next.Profiles)})
		return
	}
	recordRequestOperation(c, h.logs, "llm_profile_import", "提供商配置已导入", next.Profiles[0].ID, map[string]any{"profile_count": len(next.Profiles)})
	c.JSON(200, h.profileSetPayload(next))
}

// models 根据当前或草稿配置读取可用模型列表。
func (h *LLMConfigHandler) models(c *gin.Context) {
	cfg := h.store.Current()
	if c.Request.Method == http.MethodPost {
		// POST 用于前端在保存前拿“草稿配置”的模型列表，例如刚改了 Base URL 或 provider。
		var payload llmConfigPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			h.writeError(c, 400, "llm_models_list", err, "", nil)
			return
		}
		cfg = configFromPayload(payload)
		existing := existingProfileConfig(h.store.Profiles(), payload)
		cfg = mergeUnsubmittedLLMConfig(payload, cfg, existing)
		if cfg.APIKey == "" && existing.Provider == cfg.Provider {
			cfg.APIKey = existing.APIKey
		}
	}

	listCtx, cancel := context.WithTimeout(c.Request.Context(), llmModelListTimeout)
	defer cancel()
	models, err := h.listModels(listCtx, cfg)
	if err != nil {
		h.writeError(c, 502, "llm_models_list", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm_models_list", "LLM 模型列表已读取", cfg.Model, map[string]any{
		"provider": string(cfg.Provider),
		"model":    cfg.Model,
		"count":    len(models),
	})
	c.JSON(200, llmModelsPayload{Models: models})
}

// test 使用当前或草稿配置执行 LLM 连通测试。
func (h *LLMConfigHandler) test(c *gin.Context) {
	var payload struct {
		llmTestPayload
		llmConfigPayload
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm_test", err, "", nil)
		return
	}
	if payload.Message == "" {
		payload.Message = "ping"
	}
	testMode := strings.ToLower(strings.TrimSpace(payload.Mode))
	if testMode == "" && llm.NormalizeProfileGroup(payload.Group) == llm.GroupImage {
		testMode = "image"
	}
	if testMode != "" && testMode != "text" && testMode != "image" {
		h.writeError(c, 400, "llm_test", fmt.Errorf("unsupported test mode %q", payload.Mode), payload.Model, nil)
		return
	}

	cfg := h.store.Current()
	// 连通测试允许直接使用表单里的临时配置，成功与否不影响当前已保存配置。
	if payload.Provider != "" || payload.Model != "" || payload.BaseURL != "" || payload.APIStyle != "" || payload.APIFormat != "" || payload.APIKey != "" || payload.UserAgent != "" || payload.ImageModel != "" || payload.ImageBaseURL != "" || payload.ImageOrigin != "" || payload.ImageTimeoutMS != 0 || tokenLimitValue(payload.ContextWindowTokens) != 0 || tokenLimitValue(payload.MaxContextTokens) != 0 || tokenLimitValue(payload.MaxOutputTokens) != 0 || payload.TimeoutMS != 0 || payload.Temperature != nil || payload.ReasoningEffort != "" {
		cfg = configFromPayload(payload.llmConfigPayload)
		existing := existingProfileConfig(h.store.Profiles(), payload.llmConfigPayload)
		cfg = mergeUnsubmittedLLMConfig(payload.llmConfigPayload, cfg, existing)
		if cfg.APIKey == "" && existing.Provider == cfg.Provider {
			cfg.APIKey = existing.APIKey
		}
	}
	// image 分组里的 model 就是机器人 image 角色实际使用的模型。
	// 测试时同步到 ImageModel，避免误测 provider 的默认生图模型或文本模型。
	if testMode == "image" && strings.TrimSpace(payload.Model) != "" {
		cfg.ImageModel = strings.TrimSpace(payload.Model)
	}
	// 分组名是用户自己起的（线上就有叫「生图」的），拿它判生图会漏。配置装好之后
	// 再看一次：要测的模型就是这套配置的生图模型时，按生图测。
	if testMode == "" && strings.TrimSpace(payload.Model) != "" &&
		strings.EqualFold(strings.TrimSpace(payload.Model), strings.TrimSpace(cfg.ImageModelWithDefault())) {
		testMode = "image"
	}
	if testMode == "" {
		testMode = "text"
	}
	if testMode != "text" && testMode != "image" {
		h.writeError(c, 400, "llm_test", fmt.Errorf("unsupported test mode %q", payload.Mode), payload.Model, nil)
		return
	}
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, 400, "llm_test", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	if testMode == "image" {
		generator, ok := client.(llm.ImageGenerator)
		if !ok {
			err := fmt.Errorf("llm: image generation is not supported for provider %q", cfg.Provider)
			h.writeError(c, 400, "llm_test_image", err, cfg.ImageModelWithDefault(), llmLogMetadata(cfg, ""))
			return
		}
		started := time.Now()
		resp, err := generator.GenerateImage(c.Request.Context(), llm.ImageGenerateRequest{
			Model:  cfg.ImageModelWithDefault(),
			Prompt: payload.Message,
			N:      1,
		})
		if err != nil {
			h.writeError(c, 502, "llm_test_image", err, cfg.ImageModelWithDefault(), llmLogMetadata(cfg, ""))
			return
		}
		recordLLMUsage(c, h.logs, cfg.Provider, firstNonEmpty(resp.Model, cfg.ImageModelWithDefault()), resp.Usage, "webui_image_test", time.Since(started))
		recordRequestOperation(c, h.logs, "llm_test_image", "LLM 生图测试成功", resp.Model, llmLogMetadata(cfg, ""))
		c.JSON(200, resp)
		return
	}

	started := time.Now()
	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: payload.Message}},
	})
	if errors.Is(err, llm.ErrDecisionRequired) {
		// 判断模型（TypeSafe System One）不生成文本，发一句 ping 永远过不了，而且
		// 在出网之前就被挡掉——界面上看起来像"连不通"，其实链路一次都没试过。
		// 改成问它一道真题：能答上来就说明域名、凭据和协议都是通的。
		resp, err = client.Generate(c.Request.Context(), decisionProbeRequest(cfg, payload.Message))
	}
	if err != nil {
		h.writeError(c, 502, "llm_test", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordLLMUsage(c, h.logs, resp.Provider, firstNonEmpty(resp.Model, cfg.Model), resp.Usage, "webui_llm_test", time.Since(started))

	recordRequestOperation(c, h.logs, "llm_test", "LLM 连通测试成功", cfg.Model, llmLogMetadata(cfg, ""))
	c.JSON(200, resp)
}

// payloadFromConfig 把 LLM provider 配置转换为前端 payload。
func payloadFromConfig(cfg llm.ProviderConfig) llmConfigPayload {
	// raw 保留落库的原样：WithDefaults 会把推断出来的窗口写进字段，拿它回显就等于
	// 告诉用户「这个数是你设的」。
	raw := cfg
	cfg = cfg.WithDefaults()
	// API Key 只暴露“是否已配置”，实际值由 WithSecrets 版本在可信场景下返回。
	payload := llmConfigPayload{
		Provider:         cfg.Provider,
		APIStyle:         cfg.APIStyle,
		APIFormat:        cfg.APIFormatWithDefault(),
		APIKeyConfigured: cfg.APIKey != "",
		APIKeyPreview:    maskLLMAPIKey(cfg.APIKey),
		BaseURL:          cfg.BaseURL,
		Models:           cfg.Models,
		Model:            cfg.Model,
		ImageModel:       cfg.ImageModelWithDefault(),
		ImageBaseURL:     cfg.ImageBaseURL,
		ImageOrigin:      cfg.ImageOrigin,
		ImageTimeoutMS:   cfg.ImageTimeout.Milliseconds(),
		UserAgent:        cfg.UserAgentWithDefault(),
		Headers:          maskLLMHeaders(cfg.NormalizedHeaders()),
		Temperature:      cfg.Temperature,
		ReasoningEffort:  cfg.ReasoningEffort,
		MaxOutputTokens:  optionalTokenLimit(cfg.MaxOutputTokens),
		TimeoutMS:        cfg.Timeout.Milliseconds(),
	}
	payload.ContextWindowTokens = optionalTokenLimit(raw.ContextWindowTokens)
	payload.MaxContextTokens = optionalTokenLimit(raw.MaxContextTokens)
	window, source := raw.ResolveContextWindowTokens()
	payload.EffectiveContextWindowTokens = window
	payload.EffectiveMaxContextTokens = cfg.MaxContextTokensWithDefault()
	payload.ContextWindowSource = source
	payload.CatalogContextWindowTokens = raw.CatalogContextWindowTokens(cfg.Model)
	payload.EffectiveMaxOutputTokens, payload.MaxOutputTokensSource = cfg.ResolveMaxOutputTokens(cfg.Model)
	return payload
}

// optionalTokenLimit 把「0 表示没填」翻译成 JSON 的 null。
func optionalTokenLimit(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	limit := value
	return &limit
}

// tokenLimitValue 读回可选的 token 上限：nil 表示这次请求没提交这个字段。
func tokenLimitValue(value *int64) int64 {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

// maskLLMHeaders 回显自定义请求头时只保留键名，值一律清空。请求头里可能放着
// token（中转网关常用 x-api-key 之类的头认证），而配置接口的响应会进浏览器内存、
// 前端状态和任何抓包，跟 api_key 一样不该明文回显。保留键名是为了让界面显示
// 「这套配置已经配了哪几个头」，改值就重新填，删除就把那一行去掉。
func maskLLMHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	masked := make(map[string]string, len(headers))
	for name := range headers {
		masked[name] = ""
	}
	return masked
}

// mergeLLMHeaders 合并一次请求头提交。
//
// 值被脱敏回显成空串，所以提交上来的空值只能理解为「这个头不变」，不能当成
// 「把它改成空」——否则界面原样把读到的配置写回去就会把所有头洗掉。
//
// 键集合则以提交的为准：文本域里那些键名是用户看得见的，删掉一行就是要删掉这个
// 头。没提交 headers 字段（nil）才表示这个客户端根本没碰它，整体保留旧值。
func mergeLLMHeaders(existing, submitted map[string]string) map[string]string {
	if submitted == nil {
		return existing
	}
	merged := make(map[string]string, len(submitted))
	for name, value := range submitted {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.TrimSpace(value) == "" {
			// 空值沿用已存的那个；已存的也没有就说明这个头没有值，丢弃。
			if old, ok := existing[name]; ok && strings.TrimSpace(old) != "" {
				merged[name] = old
			}
			continue
		}
		merged[name] = value
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func maskLLMAPIKey(value string) string {
	key := []rune(strings.TrimSpace(value))
	if len(key) == 0 {
		return ""
	}
	if len(key) < 3 {
		return "••••"
	}
	prefix, suffix := 1, 1
	if len(key) >= 13 {
		prefix, suffix = 5, 4
	} else if len(key) >= 8 {
		prefix, suffix = 3, 3
	}
	return string(key[:prefix]) + "…" + string(key[len(key)-suffix:])
}

// payloadFromConfigWithSecrets 把提供商配置转换为包含密钥的 payload。
func payloadFromConfigWithSecrets(cfg llm.ProviderConfig) llmConfigPayload {
	payload := payloadFromConfig(cfg)
	payload.APIKey = cfg.APIKey
	return payload
}

// payloadFromProfile 把单个提供商配置档转换为前端 payload。
func payloadFromProfile(profile llm.Profile) llmConfigPayload {
	payload := payloadFromConfig(profile.Config)
	payload.ID = profile.ID
	payload.Name = profile.Name
	payload.Group = llm.NormalizeProfileGroup(profile.Group)
	payload.Description = profile.Description
	if !profile.UpdatedAt.IsZero() {
		payload.UpdatedAt = profile.UpdatedAt.Format(time.RFC3339)
	}
	return payload
}

// payloadFromProfileWithSecrets 把单个配置档转换为包含密钥的 payload。
func payloadFromProfileWithSecrets(profile llm.Profile) llmConfigPayload {
	payload := payloadFromConfigWithSecrets(profile.Config)
	payload.ID = profile.ID
	payload.Name = profile.Name
	payload.Group = llm.NormalizeProfileGroup(profile.Group)
	payload.Description = profile.Description
	if !profile.UpdatedAt.IsZero() {
		payload.UpdatedAt = profile.UpdatedAt.Format(time.RFC3339)
	}
	return payload
}

// profileSetPayload 在安全 payload 基础上补上模型分配的引用关系。
func (h *LLMConfigHandler) profileSetPayload(set llm.ProfileSet) llmConfigPayload {
	return h.attachRoleBindings(payloadFromProfileSet(set))
}

// attachRoleBindings 给每套配置标出「谁在用它、用的哪个模型、那个模型的窗口多大」。
func (h *LLMConfigHandler) attachRoleBindings(payload llmConfigPayload) llmConfigPayload {
	if h == nil || h.botProfiles == nil {
		return payload
	}
	bots := h.botProfiles.Profiles()
	payload.RoleBindings = botRoleBindingsFor(bots, payload.ID, payload.Group)
	for index := range payload.Profiles {
		payload.Profiles[index].RoleBindings = botRoleBindingsFor(
			bots, payload.Profiles[index].ID, payload.Profiles[index].Group)
	}
	return payload
}

// llmRoleLabels 和机器人页「模型分配」那四行用同一套说法。
var llmRoleLabels = map[string]string{
	"chat":   "对话",
	"vision": "视觉理解",
	"intent": "意图识别",
	"image":  "图片生成",
}

// botRoleBindingsFor 找出所有指向这套配置的模型分配。按 profile_id 直接指定和按
// 分组指定两种绑定都要算上。
func botRoleBindingsFor(bots assistant.ProfileSet, profileID, group string) []llmRoleBinding {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil
	}
	group = llm.NormalizeProfileGroup(group)
	bindings := make([]llmRoleBinding, 0, 4)
	for _, bot := range bots.Profiles {
		for _, role := range []string{"chat", "vision", "intent", "image"} {
			binding, ok := bot.ModelRoles[role]
			if !ok {
				continue
			}
			boundProfile := strings.TrimSpace(binding.ProfileID)
			boundGroup := llm.NormalizeProfileGroup(binding.Group)
			if boundProfile != profileID && !(boundProfile == "" && boundGroup != "" && boundGroup == group) {
				continue
			}
			model := strings.TrimSpace(binding.Model)
			if model == "" {
				continue
			}
			bindings = append(bindings, llmRoleBinding{
				BotID:     bot.ID,
				BotName:   strings.TrimSpace(bot.Name),
				Role:      role,
				RoleLabel: llmRoleLabels[role],
				Model:     model,
			})
		}
	}
	if len(bindings) == 0 {
		return nil
	}
	return bindings
}

// payloadFromProfileSet 把提供商配置集转换为前端安全 payload。
func payloadFromProfileSet(set llm.ProfileSet) llmConfigPayload {
	first, ok := set.FirstProfile()
	if !ok {
		return llmConfigPayload{}
	}
	payload := payloadFromProfile(first)
	payload.Profiles = make([]llmConfigPayload, 0, len(set.Profiles))
	for _, profile := range set.Profiles {
		payload.Profiles = append(payload.Profiles, payloadFromProfile(profile))
	}
	return payload
}

// payloadFromProfileSetWithSecrets 把配置集转换为包含密钥的导出 payload。
func payloadFromProfileSetWithSecrets(set llm.ProfileSet) llmConfigPayload {
	first, ok := set.FirstProfile()
	if !ok {
		return llmConfigPayload{}
	}
	payload := payloadFromProfileWithSecrets(first)
	payload.Profiles = make([]llmConfigPayload, 0, len(set.Profiles))
	for _, profile := range set.Profiles {
		payload.Profiles = append(payload.Profiles, payloadFromProfileWithSecrets(profile))
	}
	return payload
}

// configFromPayload 把前端 LLM payload 转回内部 provider 配置。
func configFromPayload(payload llmConfigPayload) llm.ProviderConfig {
	cfg := llm.ProviderConfig{
		Provider:            payload.Provider,
		APIStyle:            payload.APIStyle,
		APIFormat:           payload.APIFormat,
		APIKey:              payload.APIKey,
		BaseURL:             payload.BaseURL,
		Models:              payload.Models,
		Model:               payload.Model,
		ImageModel:          payload.ImageModel,
		ImageBaseURL:        payload.ImageBaseURL,
		ImageOrigin:         payload.ImageOrigin,
		ImageTimeout:        time.Duration(payload.ImageTimeoutMS) * time.Millisecond,
		UserAgent:           payload.UserAgent,
		Headers:             payload.Headers,
		Temperature:         payload.Temperature,
		ReasoningEffort:     payload.ReasoningEffort,
		ContextWindowTokens: tokenLimitValue(payload.ContextWindowTokens),
		MaxContextTokens:    tokenLimitValue(payload.MaxContextTokens),
		MaxOutputTokens:     tokenLimitValue(payload.MaxOutputTokens),
		Timeout:             time.Duration(payload.TimeoutMS) * time.Millisecond,
	}.WithDefaults()
	// An explicitly empty model asks the save handler to discover the provider's
	// model list before choosing the first available model.
	if strings.TrimSpace(payload.Model) == "" {
		cfg.Model = ""
	}
	return cfg
}

// mergeUnsubmittedLLMConfig protects advanced settings that the compact current
// editor does not expose. Legacy API clients can still submit those fields.
func mergeUnsubmittedLLMConfig(payload llmConfigPayload, cfg, existing llm.ProviderConfig) llm.ProviderConfig {
	if existing.Provider == "" || existing.Provider != cfg.Provider {
		return cfg
	}
	if payload.APIStyle == "" && payload.APIFormat == "" {
		cfg.APIStyle = existing.APIStyle
		cfg.APIFormat = existing.APIFormat
	}
	if strings.TrimSpace(payload.ImageModel) == "" {
		cfg.ImageModel = existing.ImageModel
	}
	if strings.TrimSpace(payload.ImageBaseURL) == "" {
		cfg.ImageBaseURL = existing.ImageBaseURL
	}
	if strings.TrimSpace(payload.ImageOrigin) == "" {
		cfg.ImageOrigin = existing.ImageOrigin
	}
	if payload.ImageTimeoutMS == 0 {
		cfg.ImageTimeout = existing.ImageTimeout
	}
	cfg.Headers = mergeLLMHeaders(existing.Headers, payload.Headers)
	if strings.TrimSpace(payload.ReasoningEffort) == "" {
		cfg.ReasoningEffort = existing.ReasoningEffort
	}
	// nil 才是「这个客户端没提交这个字段」；提交了 0 就是明确要求改回自动推断。
	if payload.ContextWindowTokens == nil {
		cfg.ContextWindowTokens = existing.ContextWindowTokens
	}
	if payload.MaxContextTokens == nil {
		cfg.MaxContextTokens = existing.MaxContextTokens
	}
	if payload.MaxOutputTokens == nil {
		cfg.MaxOutputTokens = existing.MaxOutputTokens
	}
	if payload.TimeoutMS == 0 {
		cfg.Timeout = existing.Timeout
	}
	return cfg.WithDefaults()
}

// existingProfileConfig 在配置集中查找 payload 对应的旧配置。
func existingProfileConfig(set llm.ProfileSet, payload llmConfigPayload) llm.ProviderConfig {
	// 只有明确指定已有 profile 才能复用其密钥：新建草稿没有 ID 就不复用，
	// 否则不同配置会表现得像共享 API Key。
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		return llm.ProviderConfig{}
	}
	for _, profile := range set.Profiles {
		if profile.ID == targetID {
			return profile.Config
		}
	}
	return llm.ProviderConfig{}
}

// upsertProfileSet 在配置集中更新现有 profile 或新增 profile。
func upsertProfileSet(set llm.ProfileSet, payload llmConfigPayload, cfg llm.ProviderConfig) llm.ProfileSet {
	now := time.Now()
	if len(set.Profiles) == 0 {
		// 首次保存时从单个 provider 配置升级为配置集。
		set = llm.NewProfileSet(cfg)
		set.Profiles[0].Name = llm.NormalizeProfileName(payload.Name)
		set.Profiles[0].Group = llm.NormalizeProfileGroup(payload.Group)
		set.Profiles[0].Description = strings.TrimSpace(payload.Description)
		set.Profiles[0].UpdatedAt = now
		return set
	}

	targetID := strings.TrimSpace(payload.ID)

	for i := range set.Profiles {
		if set.Profiles[i].ID != targetID {
			continue
		}
		if strings.TrimSpace(payload.Name) != "" {
			set.Profiles[i].Name = llm.NormalizeProfileName(payload.Name)
		}
		set.Profiles[i].Group = llm.NormalizeProfileGroup(payload.Group)
		set.Profiles[i].Description = strings.TrimSpace(payload.Description)
		set.Profiles[i].UpdatedAt = now
		set.Profiles[i].Config = cfg
		return set
	}

	newProfile := llm.Profile{
		ID:          targetID,
		Name:        llm.NormalizeProfileName(payload.Name),
		Group:       llm.NormalizeProfileGroup(payload.Group),
		Description: strings.TrimSpace(payload.Description),
		UpdatedAt:   now,
		Config:      cfg,
	}
	// 新建配置如果没有前端传入 ID，就生成稳定 UUID，后续重排/删除都靠它定位。
	if newProfile.ID == "" {
		newProfile.ID = llm.NewProfileSet(cfg).Profiles[0].ID
	}
	set.Profiles = append(set.Profiles, newProfile)
	return set
}

// firstNonEmpty 返回第一个去空白后非空的字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// queryBool 将查询参数解析为布尔值。
func queryBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// writeError 写入提供商配置接口错误日志并返回响应。
func (h *LLMConfigHandler) writeError(c *gin.Context, status int, action string, err error, target string, metadata map[string]any) {
	logAndWriteError(c, h.logs, status, action, err, target, metadata)
}

// llmLogTarget 封装当前模块的 llmLogTarget 逻辑。
func llmLogTarget(payload llmConfigPayload) string {
	return firstNonEmpty(payload.ID, payload.Name, payload.Model)
}

// llmLogMetadata 封装当前模块的 llmLogMetadata 逻辑。
func llmLogMetadata(cfg llm.ProviderConfig, profileID string) map[string]any {
	metadata := map[string]any{
		"provider": string(cfg.Provider),
		"model":    cfg.Model,
	}
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		metadata["profile_id"] = profileID
	}
	if cfg.BaseURL != "" {
		metadata["base_url"] = cfg.BaseURL
	}
	return metadata
}

// writeError 写出统一 JSON 错误响应。
func writeError(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error()})
}

// decisionProbeRequest 组一道最小的判断题，用来验判断模型的连通性。
//
// 题目本身要有意义：问的就是这类模型在 Diana 里实际负责的事（这句话是不是在跟
// 机器人说话），答案里带概率，看一眼就知道模型真的在判断，而不是在回显。
func decisionProbeRequest(cfg llm.ProviderConfig, message string) llm.GenerateRequest {
	return llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "你在判断一条群聊消息。"},
			{Role: llm.RoleUser, Content: message},
		},
		Model: cfg.Model,
		Decision: &llm.DecisionSpec{Questions: []llm.DecisionQuestion{{
			Key:            "addressed",
			Kind:           llm.DecisionNoul,
			Label:          "这句话是不是在跟机器人说话",
			Instructions:   "判断这条消息是不是在对机器人说话。",
			TrueCriteria:   "在称呼机器人、向它提问或要求它做事",
			FalseCriteria:  "在和别人说话，或者只是自言自语",
			Path:           "addressed",
			ConfidencePath: "confidence",
			ReasonPath:     "reason",
		}}},
	}
}
