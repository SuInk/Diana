// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func (h *BotHandler) codingAgentSetup(c *gin.Context) {
	var payload struct {
		Agent     string `json:"agent"`
		Operation string `json:"operation"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "配置请求无效"})
		return
	}
	if payload.Operation != "status" && payload.Operation != "install" && payload.Operation != "test" && payload.Operation != "login-start" && payload.Operation != "login-status" && payload.Operation != "login-cancel" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的配置操作"})
		return
	}
	_, settings, ok := h.runtime.Plugins().PluginForConfiguration("official.coding-agent", "")
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "编码代理不可用"})
		return
	}
	var result assistant.CodingSetupStatus
	var err error
	if strings.HasPrefix(payload.Operation, "login-") {
		result, err = assistant.CodingAgentDeviceLogin(settings, payload.Agent, payload.Operation)
	} else {
		result, err = assistant.CodingAgentSetup(c.Request.Context(), settings, payload.Agent, payload.Operation)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if payload.Operation != "status" && payload.Operation != "login-status" {
		recordRequestOperation(c, h.logs, "assistant.coding.setup", "编码代理配置操作已执行", payload.Agent, map[string]any{"operation": payload.Operation})
	}
	c.JSON(http.StatusOK, result)
}
