// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// replyContextRuntime 给机器人设置「每轮带什么」的柱形图：这台机器人各会话上一轮主回复
// 请求平均下来，按块各占多少。
type replyContextRuntime interface {
	ReplyContextOverview(profileID string) (assistant.ReplyContextBreakdown, bool)
}

func (h *BotHandler) replyContext(c *gin.Context) {
	r, ok := h.runtime.(replyContextRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "请求构成不可用"})
		return
	}
	response := gin.H{}
	if breakdown, found := r.ReplyContextOverview(c.Query("profile")); found {
		response["breakdown"] = breakdown
	}
	c.JSON(http.StatusOK, response)
}
