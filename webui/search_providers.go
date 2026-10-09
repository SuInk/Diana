// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

func (h *BotHandler) searchProviders(c *gin.Context) {
	profileID, ok := h.pluginProfileScope(c)
	if !ok {
		return
	}
	config, err := h.runtime.Plugins().SearchConfiguration(profileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "search_providers", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, config)
}

func (h *BotHandler) saveSearchProvider(c *gin.Context) {
	var provider assistant.SearchProvider
	if err := c.ShouldBindJSON(&provider); err != nil {
		h.writeError(c, http.StatusBadRequest, "search_provider_save", err, "", nil)
		return
	}
	id, err := h.runtime.Plugins().SaveSearchProvider(provider)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "search_provider_save", err, provider.ID, nil)
		return
	}
	if err := h.persistState(); err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_provider_save", fmt.Errorf("搜索提供商持久化失败：%w", err), id, nil)
		return
	}
	recordRequestOperation(c, h.logs, "search_provider_save", "搜索提供商已保存", id, map[string]any{"type": provider.Type})
	config, err := h.runtime.Plugins().SearchConfiguration("")
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_providers", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, config)
}

// exportSearchProviders 返回含密钥的搜索提供商，供「提供商」页导出文件使用。
func (h *BotHandler) exportSearchProviders(c *gin.Context) {
	providers, err := h.runtime.Plugins().ExportSearchProviders()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_providers_export", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

// importSearchProviders 按 ID 覆盖导入文件里的搜索提供商，文件外的保持不动。
func (h *BotHandler) importSearchProviders(c *gin.Context) {
	var payload struct {
		Providers []assistant.SearchProvider `json:"providers"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "search_providers_import", fmt.Errorf("导入文件格式无效"), "", nil)
		return
	}
	if err := h.runtime.Plugins().ImportSearchProviders(payload.Providers); err != nil {
		h.writeError(c, http.StatusBadRequest, "search_providers_import", err, "", nil)
		return
	}
	if err := h.persistState(); err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_providers_import", fmt.Errorf("搜索提供商持久化失败：%w", err), "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "search_providers_import", "搜索提供商已导入", "", map[string]any{"count": len(payload.Providers)})
	config, err := h.runtime.Plugins().SearchConfiguration("")
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_providers", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, config)
}

func (h *BotHandler) testSearchProvider(c *gin.Context) {
	var payload struct {
		Provider assistant.SearchProvider `json:"provider"`
		Query    string                   `json:"query"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "search_provider_test", fmt.Errorf("测试配置格式无效"), "", nil)
		return
	}
	result := h.runtime.Plugins().TestSearchProvider(c.Request.Context(), payload.Provider, payload.Query)
	c.JSON(http.StatusOK, result)
}

func (h *BotHandler) deleteSearchProvider(c *gin.Context) {
	id := c.Param("id")
	for _, profile := range h.profiles.Profiles().WithDefaults().Profiles {
		if profile.WebSearch != nil && slices.Contains(profile.WebSearch.ProviderIDs, id) {
			h.writeError(c, http.StatusConflict, "search_provider_delete", fmt.Errorf("机器人「%s」仍在使用此搜索提供商，请先更换搜索来源", profile.Name), id, nil)
			return
		}
	}
	if err := h.runtime.Plugins().DeleteSearchProvider(id); err != nil {
		h.writeError(c, http.StatusBadRequest, "search_provider_delete", err, id, nil)
		return
	}
	if err := h.persistState(); err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_provider_delete", fmt.Errorf("搜索提供商持久化失败：%w", err), id, nil)
		return
	}
	recordRequestOperation(c, h.logs, "search_provider_delete", "搜索提供商已删除", id, nil)
	config, err := h.runtime.Plugins().SearchConfiguration("")
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "search_providers", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, config)
}

func (h *BotHandler) validateSearchAssignment(value *assistant.WebSearchAssignment) error {
	if value == nil || value.Disabled {
		return nil
	}
	config, err := h.runtime.Plugins().SearchConfiguration("")
	if err != nil {
		return err
	}
	for _, id := range value.ProviderIDs {
		if !slices.ContainsFunc(config.Providers, func(provider assistant.SearchProvider) bool { return provider.ID == id && !provider.Disabled }) {
			return fmt.Errorf("搜索提供商 %s 不存在或已停用，请重新选择", id)
		}
	}
	return nil
}
