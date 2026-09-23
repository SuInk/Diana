// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"

	"github.com/SuInk/diana/model/assistant"
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
