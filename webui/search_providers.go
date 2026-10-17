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
	config, err := h.runtime.Plugins().SearchConfiguration("")
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
	if name := h.searchProviderUser(id); name != "" {
		h.writeError(c, http.StatusConflict, "search_provider_delete", fmt.Errorf("机器人「%s」的联网搜索仍在使用此提供商，请先在机器人页更换来源", name), id, nil)
		return
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

func (h *BotHandler) searchAssignment(c *gin.Context) {
	config, err := h.runtime.Plugins().SearchConfiguration("")
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "search_assignment", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, config.DefaultAssignment)
}

func (h *BotHandler) saveSearchAssignment(c *gin.Context) {
	var value assistant.WebSearchAssignment
	if err := c.ShouldBindJSON(&value); err != nil {
		h.writeError(c, http.StatusBadRequest, "search_assignment_save", err, "", nil)
		return
	}
	status := http.StatusBadRequest
	err := h.runtime.Plugins().SaveSearchAssignment(value, func(states map[string]assistant.PersistedPluginState) error {
		if h.sqlite == nil {
			return nil
		}
		if err := h.sqlite.SavePluginStates(h.ctx, states); err != nil {
			status = http.StatusInternalServerError
			return fmt.Errorf("全局搜索配置持久化失败：%w", err)
		}
		return nil
	})
	if err != nil {
		h.writeError(c, status, "search_assignment_save", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "search_assignment_save", "全局搜索配置已保存", "", nil)
	h.searchAssignment(c)
}

// validateSearchOverride 校验机器人单独搜索配置引用的提供商都存在且已启用。
// 关闭搜索时保留已选来源，不要求它们仍可用。
func (h *BotHandler) validateSearchOverride(value *assistant.WebSearchAssignment) error {
	if value == nil || value.Disabled || h.runtime == nil {
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

// searchProviderUser 返回仍在使用该提供商的机器人名；没保存过搜索配置的机器人
// 按升级前的全局路由算。
func (h *BotHandler) searchProviderUser(id string) string {
	var defaults []string
	if config, err := h.runtime.Plugins().SearchConfiguration(""); err == nil {
		defaults = config.DefaultAssignment.ProviderIDs
	}
	for _, profile := range h.profiles.Profiles().Profiles {
		ids := defaults
		if profile.WebSearchOverride != nil {
			ids = profile.WebSearchOverride.ProviderIDs
		}
		if slices.Contains(ids, id) {
			return firstNonEmptyString(profile.Name, profile.ID)
		}
	}
	return ""
}
