// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// replyContextRuntime 给机器人设置「上下文」页的占比条：哪些会话有上一轮的主回复请求，
// 某个会话那份请求按块各占多少。
type replyContextRuntime interface {
	ReplyContextSessions(profileID string) []assistant.ReplyContextSession
	ReplyContextBreakdownFor(profileID, session string) (assistant.ReplyContextBreakdown, bool)
}

// replyContext 列出会话，并给出选中会话（没选就是最近的那个）的请求构成。
func (h *BotHandler) replyContext(c *gin.Context) {
	r, ok := h.runtime.(replyContextRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "上下文占比不可用"})
		return
	}
	profileID := c.Query("profile")
	sessions := r.ReplyContextSessions(profileID)
	if sessions == nil {
		sessions = []assistant.ReplyContextSession{}
	}
	session := strings.TrimSpace(c.Query("session"))
	if session == "" && len(sessions) > 0 {
		session = sessions[0].Session
	}
	response := gin.H{"sessions": sessions}
	if session != "" {
		if breakdown, found := r.ReplyContextBreakdownFor(profileID, session); found {
			response["breakdown"] = breakdown
		}
	}
	c.JSON(http.StatusOK, response)
}
