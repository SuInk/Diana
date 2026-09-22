// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type agentResidencyRuntime interface {
	AgentResidency(profileID string) ([]assistant.AgentResidencyEntry, bool)
	SetAgentResidency(profileID, id string, resident *bool) error
	SaveAgentResidencyList(profileID string, ids []string) error
}

func (h *BotHandler) agentResidency(c *gin.Context) {
	r, ok := h.runtime.(agentResidencyRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "常驻档位不可用"})
		return
	}
	items, listed := r.AgentResidency(c.Query("profile"))
	if items == nil {
		items = []assistant.AgentResidencyEntry{}
	}
	// listed 说的是「这台机器人有没有自己的名单」：没有就跟着内置推荐走，界面据此
	// 决定一个不在名单里的工具该显示成「不常驻」还是「推荐带上」。
	c.JSON(http.StatusOK, gin.H{"items": items, "listed": listed})
}

func (h *BotHandler) setAgentResidency(c *gin.Context) {
	r, ok := h.runtime.(agentResidencyRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "常驻档位不可用"})
		return
	}
	var payload struct {
		ProfileID string `json:"profile_id"`
		// IDs 带上就是整份名单；界面就是这么存的，一次写完，不留中间态。
		IDs []string `json:"ids"`
		// Reset 表示退回内置推荐名单，往后跟着版本走。
		Reset bool `json:"reset"`
		// 下面两个是就地加一个 / 删一个，给模型那条管理工具用。
		ID       string `json:"id"`
		Resident *bool  `json:"resident"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "常驻名单请求格式错误"})
		return
	}
	if payload.Reset || payload.IDs != nil {
		ids := payload.IDs
		if payload.Reset {
			ids = nil
		}
		if err := r.SaveAgentResidencyList(payload.ProfileID, ids); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		recordRequestOperation(c, h.logs, "agent_residency", "常驻名单已更新", payload.ProfileID, map[string]any{"profile_id": payload.ProfileID, "count": len(ids), "reset": payload.Reset})
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if err := r.SetAgentResidency(payload.ProfileID, payload.ID, payload.Resident); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	recordRequestOperation(c, h.logs, "agent_residency", "常驻名单已更新", payload.ID, map[string]any{"profile_id": payload.ProfileID, "resident": payload.Resident})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
