// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// replyContextRuntime 给机器人设置「上下文」页的占比条：这台机器人各会话上一轮主回复
// 请求平均下来，按块各占多少。
type replyContextRuntime interface {
	ReplyContextOverview(profileID string) (assistant.ReplyContextBreakdown, bool, error)
}

func (h *BotHandler) replyContext(c *gin.Context) {
	r, ok := h.runtime.(replyContextRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "上下文占比不可用"})
		return
	}
	breakdown, found, err := r.ReplyContextOverview(c.Query("profile"))
	if err != nil {
		// 实际请求和续接前缀对不上：如实报错，不画一张和真实组装不符的图。
		c.JSON(http.StatusConflict, gin.H{"error": "请求构成对账失败：" + err.Error()})
		return
	}
	response := gin.H{}
	if found {
		response["breakdown"] = breakdown
	}
	c.JSON(http.StatusOK, response)
}
