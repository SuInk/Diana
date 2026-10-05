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
