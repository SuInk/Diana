// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"net/http"

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
