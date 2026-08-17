// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

type LLMConfigHandler struct {
	store      LLMProfileStore
	newClient  LLMClientFactory
	listModels LLMModelListFactory
	logs       AppLogWriter
}

type LLMClientFactory func(llm.ProviderConfig) (llm.LLMClient, error)
type LLMModelListFactory func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error)

type llmConfigPayload struct {
	ID                  string             `json:"id,omitempty"`
	Name                string             `json:"name,omitempty"`
	Group               string             `json:"group,omitempty"`
	Description         string             `json:"description,omitempty"`
	UpdatedAt           string             `json:"updated_at,omitempty"`
	ActiveProfileID     string             `json:"active_profile_id,omitempty"`
	Profiles            []llmConfigPayload `json:"profiles,omitempty"`
	Provider            llm.Provider       `json:"provider"`
	APIStyle            llm.APIStyle       `json:"api_style,omitempty"`
	APIFormat           llm.APIFormat      `json:"api_format,omitempty"`
	APIKey              string             `json:"api_key,omitempty"`
	APIKeyConfigured    bool               `json:"api_key_configured,omitempty"`
	APIKeyPreview       string             `json:"api_key_preview,omitempty"`
	BaseURL             string             `json:"base_url,omitempty"`
	Models              []llm.ModelInfo    `json:"models,omitempty"`
	Model               string             `json:"model"`
	ImageModel          string             `json:"image_model,omitempty"`
	ImageBaseURL        string             `json:"image_base_url,omitempty"`
	ImageOrigin         string             `json:"image_origin,omitempty"`
	ImageTimeoutMS      int64              `json:"image_timeout_ms,omitempty"`
	UserAgent           string             `json:"user_agent,omitempty"`
	Headers             map[string]string  `json:"headers,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	ContextWindowTokens int64              `json:"context_window_tokens,omitempty"`
	MaxContextTokens    int64              `json:"max_context_tokens,omitempty"`
	MaxOutputTokens     int64              `json:"max_output_tokens,omitempty"`
	TimeoutMS           int64              `json:"timeout_ms,omitempty"`
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
func NewLLMConfigHandler(store LLMProfileStore) *LLMConfigHandler {
	return NewLLMConfigHandlerWithFactory(store, func(cfg llm.ProviderConfig) (llm.LLMClient, error) {
		return llm.NewClient(cfg)
	})
}

// NewLLMConfigHandlerWithFactory 创建 LLMConfigHandler 实例。
func NewLLMConfigHandlerWithFactory(store LLMProfileStore, factory LLMClientFactory) *LLMConfigHandler {
	return &LLMConfigHandler{
		store:     store,
		newClient: factory,
		listModels: func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
			return llm.ListModels(ctx, cfg)
		},
	}
}

// SetModelListFactory 注入模型列表读取实现。
func (h *LLMConfigHandler) SetModelListFactory(factory LLMModelListFactory) {
	h.listModels = factory
}

// SetLogStore 注入 LLM 配置接口的日志写入器。
func (h *LLMConfigHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册 LLM 配置、配置集、模型列表和测试接口。
func (h *LLMConfigHandler) Register(router gin.IRouter) {
	router.GET("/api/llm/config", h.getConfig)
	router.GET("/api/llm/config/export", h.exportConfig)
	router.POST("/api/llm/config", h.saveConfig)
	router.POST("/api/llm/config/activate", h.activateProfile)
	router.POST("/api/llm/config/clone", h.cloneProfile)
	router.POST("/api/llm/config/delete", h.deleteProfile)
	router.POST("/api/llm/config/import", h.importProfiles)
	router.POST("/api/llm/config/reorder", h.reorderProfiles)
	router.GET("/api/llm/models", h.models)
	router.POST("/api/llm/models", h.models)
	router.POST("/api/llm/test", h.test)
	router.GET("/api/llm/providers", h.providers)
	router.POST("/api/llm/providers/models", h.providerModels)
	router.POST("/api/llm/providers/test", h.providerTest)
}

// providers exposes the provider/model view used by the new management UI.
// It is derived from legacy profiles during the compatibility period and never
// serializes API keys.
func (h *LLMConfigHandler) providers(c *gin.Context) {
	registry, _, err := llm.NewProviderRegistryFromProfiles(h.store.Profiles())
	if err != nil {
		h.writeError(c, http.StatusUnprocessableEntity, "llm.providers", err, "", nil)
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
		h.writeError(c, 400, "llm.providers.models", err, "", nil)
		return
	}
	registry, _, err := llm.NewProviderRegistryFromProfiles(h.store.Profiles())
	if err != nil {
		h.writeError(c, 422, "llm.providers.models", err, payload.ProviderID, nil)
		return
	}
	models, err := registry.ListModels(c.Request.Context(), payload.ProviderID)
	if err != nil {
		h.writeError(c, 502, "llm.providers.models", err, payload.ProviderID, nil)
		return
	}
	c.JSON(http.StatusOK, llmModelsPayload{Models: models})
}

func (h *LLMConfigHandler) providerTest(c *gin.Context) {
	var payload providerSelectionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.providers.test", err, "", nil)
		return
	}
	registry, _, err := llm.NewProviderRegistryFromProfiles(h.store.Profiles())
	if err != nil {
		h.writeError(c, 422, "llm.providers.test", err, payload.ProviderID, nil)
		return
	}
	if strings.TrimSpace(payload.Message) == "" {
		payload.Message = "ping"
	}
	response, err := registry.Generate(c.Request.Context(), llm.AgentModelConfig{ProviderID: payload.ProviderID, ModelID: payload.ModelID}, llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: payload.Message}}})
	if err != nil {
		h.writeError(c, 502, "llm.providers.test", err, payload.ModelID, nil)
		return
	}
	c.JSON(http.StatusOK, response)
}

// getConfig 处理 LLM 配置读取请求。
func (h *LLMConfigHandler) getConfig(c *gin.Context) {
	// 默认响应不带 API Key；本地配置页需要编辑时显式带 include_secrets=true。
	if queryBool(c.Query("include_secrets")) {
		c.JSON(200, payloadFromProfileSetWithSecrets(h.store.Profiles()))
		return
	}
	c.JSON(200, payloadFromProfileSet(h.store.Profiles()))
}

// exportConfig 导出包含密钥的 LLM 配置集。
func (h *LLMConfigHandler) exportConfig(c *gin.Context) {
	c.JSON(200, payloadFromProfileSetWithSecrets(h.store.Profiles()))
}

// saveConfig 保存当前 LLM 配置或新增配置档。
func (h *LLMConfigHandler) saveConfig(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.config.save", err, "", nil)
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
		h.writeError(c, 400, "llm.config.save", fmt.Errorf("api_key must be at least %d characters", minLLMAPIKeyChars), llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
		return
	}
	if err := cfg.ValidateChannel(); err != nil {
		h.writeError(c, 400, "llm.config.save", err, llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
		return
	}
	if strings.TrimSpace(cfg.Model) == "" {
		if len(cfg.Models) == 0 {
			models, err := h.listModels(c.Request.Context(), cfg)
			if err != nil {
				h.writeError(c, 502, "llm.config.save.models", err, llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
				return
			}
			cfg.Models = models
		}
		if len(cfg.Models) == 0 || strings.TrimSpace(cfg.Models[0].ID) == "" {
			h.writeError(c, 422, "llm.config.save.models", fmt.Errorf("provider returned no usable models"), llmLogTarget(payload), llmLogMetadata(cfg, payload.ID))
			return
		}
		cfg.Model = strings.TrimSpace(cfg.Models[0].ID)
	}

	next := upsertProfileSet(set, payload, cfg)
	h.store.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "llm.config.save", "LLM 配置已保存", next.ActiveID, llmLogMetadata(cfg, next.ActiveID))
	c.JSON(200, payloadFromProfileSet(next))
}

// activateProfile 切换当前激活的 LLM 配置档。
func (h *LLMConfigHandler) activateProfile(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.profile.activate", err, "", nil)
		return
	}
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		h.writeError(c, 400, "llm.profile.activate", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	set := h.store.Profiles().WithActive(targetID)
	current, ok := set.Current()
	if !ok || current.ID != targetID {
		h.writeError(c, 404, "llm.profile.activate", fmt.Errorf("profile %q not found", targetID), targetID, nil)
		return
	}
	h.store.SaveProfiles(set)
	recordRequestOperation(c, h.logs, "llm.profile.activate", "LLM 配置已切换", targetID, llmLogMetadata(current.Config, targetID))
	c.JSON(200, payloadFromProfileSet(set))
}

// reorderProfiles 按给定 ID 顺序重排配置档；组内顺序即失败降级的优先级。
func (h *LLMConfigHandler) reorderProfiles(c *gin.Context) {
	var payload struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.profile.reorder", err, "", nil)
		return
	}
	if len(payload.IDs) == 0 {
		h.writeError(c, 400, "llm.profile.reorder", fmt.Errorf("ids is required"), "", nil)
		return
	}
	set := h.store.Profiles().Reorder(payload.IDs)
	h.store.SaveProfiles(set)
	recordRequestOperation(c, h.logs, "llm.profile.reorder", "LLM 配置优先级已调整", "", nil)
	c.JSON(200, payloadFromProfileSet(set))
}

// deleteProfile 删除指定 LLM 配置档。
func (h *LLMConfigHandler) deleteProfile(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.profile.delete", err, "", nil)
		return
	}
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		h.writeError(c, 400, "llm.profile.delete", fmt.Errorf("profile id is required"), "", nil)
		return
	}
	set := h.store.Profiles()
	if len(set.Profiles) <= 1 {
		h.writeError(c, 400, "llm.profile.delete", fmt.Errorf("at least one llm profile must remain"), targetID, nil)
		return
	}
	next := set.Delete(targetID)
	if len(next.Profiles) == len(set.Profiles) {
		h.writeError(c, 404, "llm.profile.delete", fmt.Errorf("profile %q not found", targetID), targetID, nil)
		return
	}
	h.store.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "llm.profile.delete", "LLM 配置已删除", targetID, map[string]any{"profile_id": targetID})
	c.JSON(200, payloadFromProfileSet(next))
}

// cloneProfile 复制指定 LLM 配置档。
func (h *LLMConfigHandler) cloneProfile(c *gin.Context) {
	var payload llmConfigPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.profile.clone", err, "", nil)
		return
	}
	sourceID := strings.TrimSpace(payload.ID)
	if sourceID == "" {
		sourceID = h.store.Profiles().ActiveID
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
		h.store.SaveProfiles(next)
		recordRequestOperation(c, h.logs, "llm.profile.clone", "LLM 配置已复制", sourceID, llmLogMetadata(profile.Config, sourceID))
		c.JSON(200, payloadFromProfileSet(next))
		return
	}
	h.writeError(c, 404, "llm.profile.clone", fmt.Errorf("profile %q not found", sourceID), sourceID, nil)
}

// importProfiles 导入一组 LLM 配置档。
func (h *LLMConfigHandler) importProfiles(c *gin.Context) {
	var payload struct {
		ActiveProfileID string             `json:"active_profile_id,omitempty"`
		Profiles        []llmConfigPayload `json:"profiles"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, 400, "llm.profile.import", err, "", nil)
		return
	}
	if len(payload.Profiles) == 0 {
		h.writeError(c, 400, "llm.profile.import", fmt.Errorf("profiles are required"), "", nil)
		return
	}
	next := llm.ProfileSet{Profiles: make([]llm.Profile, 0, len(payload.Profiles))}
	seenIDs := make(map[string]struct{}, len(payload.Profiles))
	for _, item := range payload.Profiles {
		// 导入文件必须自带密钥，避免导入后看似成功但实际无法调用模型。
		cfg := configFromPayload(item)
		if cfg.APIKey == "" {
			h.writeError(c, 400, "llm.profile.import", fmt.Errorf("profile %q missing api_key", firstNonEmpty(item.Name, item.ID)), firstNonEmpty(item.ID, item.Name), nil)
			return
		}
		if err := cfg.ValidateChannel(); err != nil {
			h.writeError(c, 400, "llm.profile.import", err, firstNonEmpty(item.ID, item.Name), llmLogMetadata(cfg, item.ID))
			return
		}
		id := firstNonEmpty(strings.TrimSpace(item.ID), llm.NewProfileSet(cfg).ActiveID)
		if _, ok := seenIDs[id]; ok {
			h.writeError(c, 400, "llm.profile.import", fmt.Errorf("duplicate profile id %q", id), id, nil)
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
	next.ActiveID = firstNonEmpty(payload.ActiveProfileID, next.Profiles[0].ID)
	if current, ok := next.Current(); !ok || current.ID == "" {
		next.ActiveID = next.Profiles[0].ID
	}
	h.store.SaveProfiles(next)
	recordRequestOperation(c, h.logs, "llm.profile.import", "LLM 配置已导入", next.ActiveID, map[string]any{"profile_count": len(next.Profiles), "active_profile_id": next.ActiveID})
	c.JSON(200, payloadFromProfileSet(next))
}

// models 根据当前或草稿配置读取可用模型列表。
func (h *LLMConfigHandler) models(c *gin.Context) {
	cfg := h.store.Current()
	if c.Request.Method == http.MethodPost {
		// POST 用于前端在保存前拿“草稿配置”的模型列表，例如刚改了 Base URL 或 provider。
		var payload llmConfigPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			h.writeError(c, 400, "llm.models.list", err, "", nil)
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
		h.writeError(c, 502, "llm.models.list", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	recordRequestOperation(c, h.logs, "llm.models.list", "LLM 模型列表已读取", cfg.Model, map[string]any{
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
		h.writeError(c, 400, "llm.test", err, "", nil)
		return
	}
	if payload.Message == "" {
		payload.Message = "ping"
	}
	testMode := strings.ToLower(strings.TrimSpace(payload.Mode))
	if testMode == "" && llm.NormalizeProfileGroup(payload.Group) == llm.GroupImage {
		testMode = "image"
	}
	if testMode == "" {
		testMode = "text"
	}
	if testMode != "text" && testMode != "image" {
		h.writeError(c, 400, "llm.test", fmt.Errorf("unsupported test mode %q", payload.Mode), payload.Model, nil)
		return
	}

	cfg := h.store.Current()
	// 连通测试允许直接使用表单里的临时配置，成功与否不影响当前已保存配置。
	if payload.Provider != "" || payload.Model != "" || payload.BaseURL != "" || payload.APIStyle != "" || payload.APIFormat != "" || payload.APIKey != "" || payload.UserAgent != "" || payload.ImageModel != "" || payload.ImageBaseURL != "" || payload.ImageOrigin != "" || payload.ImageTimeoutMS != 0 || payload.ContextWindowTokens != 0 || payload.MaxContextTokens != 0 || payload.MaxOutputTokens != 0 || payload.TimeoutMS != 0 || payload.Temperature != nil || payload.ReasoningEffort != "" {
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
	client, err := h.newClient(cfg)
	if err != nil {
		h.writeError(c, 400, "llm.test", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}
	if testMode == "image" {
		generator, ok := client.(llm.ImageGenerator)
		if !ok {
			err := fmt.Errorf("llm: image generation is not supported for provider %q", cfg.Provider)
			h.writeError(c, 400, "llm.test.image", err, cfg.ImageModelWithDefault(), llmLogMetadata(cfg, ""))
			return
		}
		resp, err := generator.GenerateImage(c.Request.Context(), llm.ImageGenerateRequest{
			Model:  cfg.ImageModelWithDefault(),
			Prompt: payload.Message,
			N:      1,
		})
		if err != nil {
			h.writeError(c, 502, "llm.test.image", err, cfg.ImageModelWithDefault(), llmLogMetadata(cfg, ""))
			return
		}
		recordRequestOperation(c, h.logs, "llm.test.image", "LLM 生图测试成功", resp.Model, llmLogMetadata(cfg, ""))
		c.JSON(200, resp)
		return
	}

	resp, err := client.Generate(c.Request.Context(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: payload.Message}},
	})
	if err != nil {
		h.writeError(c, 502, "llm.test", err, cfg.Model, llmLogMetadata(cfg, ""))
		return
	}

	recordRequestOperation(c, h.logs, "llm.test", "LLM 连通测试成功", cfg.Model, llmLogMetadata(cfg, ""))
	c.JSON(200, resp)
}

// payloadFromConfig 把 LLM provider 配置转换为前端 payload。
func payloadFromConfig(cfg llm.ProviderConfig) llmConfigPayload {
	cfg = cfg.WithDefaults()
	// API Key 只暴露“是否已配置”，实际值由 WithSecrets 版本在可信场景下返回。
	payload := llmConfigPayload{
		Provider:            cfg.Provider,
		APIStyle:            cfg.APIStyle,
		APIFormat:           cfg.APIFormatWithDefault(),
		APIKeyConfigured:    cfg.APIKey != "",
		APIKeyPreview:       maskLLMAPIKey(cfg.APIKey),
		BaseURL:             cfg.BaseURL,
		Models:              cfg.Models,
		Model:               cfg.Model,
		ImageModel:          cfg.ImageModelWithDefault(),
		ImageBaseURL:        cfg.ImageBaseURL,
		ImageOrigin:         cfg.ImageOrigin,
		ImageTimeoutMS:      cfg.ImageTimeout.Milliseconds(),
		UserAgent:           cfg.UserAgentWithDefault(),
		Headers:             cfg.NormalizedHeaders(),
		Temperature:         cfg.Temperature,
		ReasoningEffort:     cfg.ReasoningEffort,
		ContextWindowTokens: cfg.ContextWindowTokens,
		MaxContextTokens:    cfg.MaxContextTokensWithDefault(),
		MaxOutputTokens:     cfg.MaxOutputTokens,
		TimeoutMS:           cfg.Timeout.Milliseconds(),
	}
	return payload
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

// payloadFromConfigWithSecrets 把 LLM 配置转换为包含密钥的 payload。
func payloadFromConfigWithSecrets(cfg llm.ProviderConfig) llmConfigPayload {
	payload := payloadFromConfig(cfg)
	payload.APIKey = cfg.APIKey
	return payload
}

// payloadFromProfile 把单个 LLM 配置档转换为前端 payload。
func payloadFromProfile(profile llm.Profile, activeID string) llmConfigPayload {
	payload := payloadFromConfig(profile.Config)
	payload.ID = profile.ID
	payload.Name = profile.Name
	payload.Group = llm.NormalizeProfileGroup(profile.Group)
	payload.Description = profile.Description
	if !profile.UpdatedAt.IsZero() {
		payload.UpdatedAt = profile.UpdatedAt.Format(time.RFC3339)
	}
	payload.ActiveProfileID = activeID
	return payload
}

// payloadFromProfileWithSecrets 把单个配置档转换为包含密钥的 payload。
func payloadFromProfileWithSecrets(profile llm.Profile, activeID string) llmConfigPayload {
	payload := payloadFromConfigWithSecrets(profile.Config)
	payload.ID = profile.ID
	payload.Name = profile.Name
	payload.Group = llm.NormalizeProfileGroup(profile.Group)
	payload.Description = profile.Description
	if !profile.UpdatedAt.IsZero() {
		payload.UpdatedAt = profile.UpdatedAt.Format(time.RFC3339)
	}
	payload.ActiveProfileID = activeID
	return payload
}

// payloadFromProfileSet 把 LLM 配置集转换为前端安全 payload。
func payloadFromProfileSet(set llm.ProfileSet) llmConfigPayload {
	current, ok := set.Current()
	if !ok {
		return llmConfigPayload{}
	}
	payload := payloadFromProfile(current, set.ActiveID)
	payload.Profiles = make([]llmConfigPayload, 0, len(set.Profiles))
	for _, profile := range set.Profiles {
		payload.Profiles = append(payload.Profiles, payloadFromProfile(profile, set.ActiveID))
	}
	return payload
}

// payloadFromProfileSetWithSecrets 把配置集转换为包含密钥的导出 payload。
func payloadFromProfileSetWithSecrets(set llm.ProfileSet) llmConfigPayload {
	current, ok := set.Current()
	if !ok {
		return llmConfigPayload{}
	}
	payload := payloadFromProfileWithSecrets(current, set.ActiveID)
	payload.Profiles = make([]llmConfigPayload, 0, len(set.Profiles))
	for _, profile := range set.Profiles {
		payload.Profiles = append(payload.Profiles, payloadFromProfileWithSecrets(profile, set.ActiveID))
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
		ContextWindowTokens: payload.ContextWindowTokens,
		MaxContextTokens:    payload.MaxContextTokens,
		MaxOutputTokens:     payload.MaxOutputTokens,
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
	if payload.Headers == nil {
		cfg.Headers = existing.Headers
	}
	if strings.TrimSpace(payload.ReasoningEffort) == "" {
		cfg.ReasoningEffort = existing.ReasoningEffort
	}
	if payload.ContextWindowTokens == 0 {
		cfg.ContextWindowTokens = existing.ContextWindowTokens
	}
	if payload.MaxContextTokens == 0 {
		cfg.MaxContextTokens = existing.MaxContextTokens
	}
	if payload.TimeoutMS == 0 {
		cfg.Timeout = existing.Timeout
	}
	return cfg.WithDefaults()
}

// existingProfileConfig 在配置集中查找 payload 对应的旧配置。
func existingProfileConfig(set llm.ProfileSet, payload llmConfigPayload) llm.ProviderConfig {
	// 只有明确指定已有 profile 才能复用其密钥。新建草稿没有 ID，
	// 不能回退到当前激活配置，否则不同配置会表现得像共享 API Key。
	targetID := strings.TrimSpace(payload.ID)
	if targetID == "" {
		targetID = strings.TrimSpace(payload.ActiveProfileID)
	}
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
	if targetID != "" {
		targetID = strings.TrimSpace(payload.ID)
	} else if len(set.Profiles) == 0 {
		targetID = set.ActiveID
	}

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
		set.ActiveID = set.Profiles[i].ID
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
	// 新建配置如果没有前端传入 ID，就生成稳定 UUID，后续切换/删除都靠它定位。
	if newProfile.ID == "" {
		newProfile.ID = llm.NewProfileSet(cfg).ActiveID
	}
	set.Profiles = append(set.Profiles, newProfile)
	set.ActiveID = newProfile.ID
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

// writeError 写入 LLM 配置接口错误日志并返回响应。
func (h *LLMConfigHandler) writeError(c *gin.Context, status int, action string, err error, target string, metadata map[string]any) {
	logAndWriteError(c, h.logs, status, action, err, target, metadata)
}

// llmLogTarget 封装当前模块的 llmLogTarget 逻辑。
func llmLogTarget(payload llmConfigPayload) string {
	return firstNonEmpty(payload.ID, payload.ActiveProfileID, payload.Name, payload.Model)
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
