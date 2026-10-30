// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type joinStrategyRuntime interface {
	QQOfficialJoinStrategy(ctx context.Context, profileID string, req assistant.QQJoinStrategyRequest) (map[string]any, error)
}

// qqJoinStrategy 管理 QQ 官方机器人的入群自动审批策略，一个入口按 op 分派。
func (h *BotHandler) qqJoinStrategy(c *gin.Context) {
	profileID := botProfileScope(c)
	if profileID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定机器人"})
		return
	}
	runtime, ok := h.runtime.(joinStrategyRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "机器人运行时不支持入群自动审批策略"})
		return
	}
	var req assistant.QQJoinStrategyRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Op == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对"})
		return
	}
	data, err := runtime.QQOfficialJoinStrategy(c.Request.Context(), profileID, req)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "qq_join_strategy", err, profileID, nil)
		return
	}
	if req.Op != "list" {
		recordRequestOperation(c, h.logs, "qq_join_strategy", "入群自动审批策略："+req.Op, profileID, nil)
	}
	c.JSON(http.StatusOK, data)
}
