// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type agentResidencyRuntime interface {
	AgentResidency(profileID string) []assistant.AgentResidencyEntry
	SetAgentResidency(profileID, id string, resident *bool) error
}

func (h *BotHandler) agentResidency(c *gin.Context) {
	r, ok := h.runtime.(agentResidencyRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "常驻档位不可用"})
		return
	}
	items := r.AgentResidency(c.Query("profile"))
	if items == nil {
		items = []assistant.AgentResidencyEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *BotHandler) setAgentResidency(c *gin.Context) {
	r, ok := h.runtime.(agentResidencyRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "常驻档位不可用"})
		return
	}
	var payload struct {
		ProfileID string `json:"profile_id"`
		ID        string `json:"id"`
		// Resident 不传表示跟随默认档。
		Resident *bool `json:"resident"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "档位请求格式错误"})
		return
	}
	if err := r.SetAgentResidency(payload.ProfileID, payload.ID, payload.Resident); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	recordRequestOperation(c, h.logs, "agent_residency", "常驻档位已更新", payload.ID, map[string]any{"profile_id": payload.ProfileID, "resident": payload.Resident})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
