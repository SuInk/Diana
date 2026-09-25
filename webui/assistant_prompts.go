// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/version"
	"github.com/gin-gonic/gin"
)

// promptCatalog 返回全部可覆盖的内置提示词：分组、默认正文、占位符和锁定的输出格式。
// 覆盖值本身跟着机器人配置走（prompt_overrides），这里只给界面一份「原文是什么」。
func (h *BotHandler) promptCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"groups":    assistant.PromptGroups(),
		"prompts":   assistant.PromptSpecs(),
		"max_runes": assistant.PromptOverrideMaxRunes,
	})
}

// exportPromptFile 把界面上当前的覆盖表（可能还没保存）渲染成一份完整的提示词 YAML。
// 从请求里拿覆盖表而不是去读已保存的配置：用户在编辑器里改了一半想先导出看看，
// 导出的应该是他眼前的那一份。
func (h *BotHandler) exportPromptFile(c *gin.Context) {
	var payload struct {
		Overrides assistant.PromptOverrides `json:"overrides"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "prompts_export", err, "", nil)
		return
	}
	raw, err := assistant.RenderPromptFile(payload.Overrides, version.Source())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "prompts_export", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"yaml": string(raw)})
}

// importPromptFile 只解析不保存：结果填回编辑器，保存配置时才生效。
func (h *BotHandler) importPromptFile(c *gin.Context) {
	var payload struct {
		Source string `json:"source"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "prompts_import", err, "", nil)
		return
	}
	result, err := assistant.ParsePromptFile([]byte(strings.TrimSpace(payload.Source)))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "prompts_import", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, result)
}
