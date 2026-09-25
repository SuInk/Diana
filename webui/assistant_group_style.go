// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/SuInk/diana/model/assistant"
)

// 群管理页上的「本群说话风格」：看模型学到的那段、手动改、点一下重新学，或者本群单独关掉。

// groupStyleRuntime 做成可选接口：测试里的假运行时不必为此实现三个空方法。
type groupStyleRuntime interface {
	GroupStyleForProfile(ctx context.Context, profileID, groupID string) (assistant.GroupStyle, bool, error)
	SaveGroupStyleForProfile(ctx context.Context, profileID, groupID, text string) (assistant.GroupStyle, bool, error)
	RelearnGroupStyle(ctx context.Context, profileID, groupID string) (assistant.GroupStyle, error)
	SetGroupStyleEnabledForProfile(ctx context.Context, profileID, groupID string, enabled bool) (assistant.GroupStyle, bool, error)
}

// groupStyleRelearnTimeout 是控制台上「重新学习」等模型的上限：读几百条消息写一段话，
// 后台模型慢的时候也该在一分多钟内回来。
const groupStyleRelearnTimeout = 2 * time.Minute

type groupStyleResponse struct {
	Style           *assistant.GroupStyle `json:"style,omitempty"`
	LearningEnabled bool                  `json:"learning_enabled"`
	MaxRunes        int                   `json:"max_runes"`
}

type groupStyleSavePayload struct {
	Text string `json:"text"`
}

type groupStyleEnabledPayload struct {
	Enabled *bool `json:"enabled"`
}

func (h *BotHandler) registerGroupStyleRoutes(router gin.IRouter) {
	router.GET("/api/assistant/groups/:id/style", h.getGroupStyle)
	router.PUT("/api/assistant/groups/:id/style", h.saveGroupStyle)
	router.POST("/api/assistant/groups/:id/style/relearn", h.relearnGroupStyle)
	router.PUT("/api/assistant/groups/:id/style/enabled", h.setGroupStyleEnabled)
}

// groupStyleTarget 取出这次请求针对的机器人和群。机器人 ID 留空且只有一台时就是它。
func (h *BotHandler) groupStyleTarget(c *gin.Context) (groupStyleRuntime, string, string, bool) {
	groupID := strings.TrimSpace(c.Param("id"))
	if groupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少群号"})
		return nil, "", "", false
	}
	runtime, ok := h.runtime.(groupStyleRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "当前运行时不支持风格学习"})
		return nil, "", "", false
	}
	profileID := strings.TrimSpace(c.Query("bot_profile_id"))
	if profileID == "" {
		profileID = strings.TrimSpace(h.runtime.ProfileConfig("").ID)
	}
	return runtime, profileID, groupID, true
}

func (h *BotHandler) groupStyleResponse(profileID string, style assistant.GroupStyle, found bool) groupStyleResponse {
	response := groupStyleResponse{MaxRunes: assistant.GroupStyleMaxRunes}
	if cfg := h.runtime.ProfileConfig(profileID); cfg.ExpressionLearningEnabled != nil {
		response.LearningEnabled = *cfg.ExpressionLearningEnabled
	}
	if found {
		response.Style = &style
	}
	return response
}

func (h *BotHandler) getGroupStyle(c *gin.Context) {
	runtime, profileID, groupID, ok := h.groupStyleTarget(c)
	if !ok {
		return
	}
	style, found, err := runtime.GroupStyleForProfile(c.Request.Context(), profileID, groupID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.groupStyleResponse(profileID, style, found))
}

// saveGroupStyle 保存手动改的风格笔记；正文为空表示交回自动学习。
func (h *BotHandler) saveGroupStyle(c *gin.Context) {
	runtime, profileID, groupID, ok := h.groupStyleTarget(c)
	if !ok {
		return
	}
	var payload groupStyleSavePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对"})
		return
	}
	if len([]rune(strings.TrimSpace(payload.Text))) > assistant.GroupStyleMaxRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "风格笔记太长了"})
		return
	}
	style, found, err := runtime.SaveGroupStyleForProfile(c.Request.Context(), profileID, groupID, payload.Text)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.groupStyleResponse(profileID, style, found))
}

func (h *BotHandler) relearnGroupStyle(c *gin.Context) {
	runtime, profileID, groupID, ok := h.groupStyleTarget(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), groupStyleRelearnTimeout)
	defer cancel()
	style, err := runtime.RelearnGroupStyle(ctx, profileID, groupID)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, assistant.ErrGroupStyleNotEnoughMessages) || errors.Is(err, assistant.ErrGroupStyleBusy) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.groupStyleResponse(profileID, style, true))
}

// setGroupStyleEnabled 单独打开或关掉一个群的风格，写好的笔记留着。
func (h *BotHandler) setGroupStyleEnabled(c *gin.Context) {
	runtime, profileID, groupID, ok := h.groupStyleTarget(c)
	if !ok {
		return
	}
	var payload groupStyleEnabledPayload
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对"})
		return
	}
	style, found, err := runtime.SetGroupStyleEnabledForProfile(c.Request.Context(), profileID, groupID, *payload.Enabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.groupStyleResponse(profileID, style, found))
}
