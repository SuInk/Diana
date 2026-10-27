// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 聊天页：主人在 WebUI 里和机器人私聊。和语音通话走同一条「主人私聊」回复流程，
// 人设、记忆、历史和主人在平台上的私聊是同一份；回复不发到平台，流式交回页面。
const (
	webChatReplyTimeout = 5 * time.Minute
	webChatMaxText      = 4000
)

type webChatRuntime interface {
	WebChatInfo(string) assistant.WebChatInfo
	WebChatHistory(context.Context, string, int) ([]assistant.WebChatMessage, error)
	WebChatSend(context.Context, string, string, func(assistant.WebChatMessage)) (assistant.WebChatMessage, error)
}

func (h *BotHandler) registerWebChatRoutes(router gin.IRouter) {
	group := router.Group("/api/assistant/chat", func(c *gin.Context) {
		if !c.GetBool("webui_admin") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "需要 WebUI 管理员登录", "auth_required": true})
			return
		}
		c.Next()
	})
	group.GET("", h.webChatHistory)
	group.POST("", h.webChatSend)
}

func (h *BotHandler) webChatRuntime(c *gin.Context) (webChatRuntime, bool) {
	runtime, ok := h.runtime.(webChatRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "运行时不支持聊天"})
	}
	return runtime, ok
}

// webChatHistory 返回能不能聊和最近的私聊记录。
func (h *BotHandler) webChatHistory(c *gin.Context) {
	runtime, ok := h.webChatRuntime(c)
	if !ok {
		return
	}
	profile := strings.TrimSpace(c.Query("profile"))
	info := runtime.WebChatInfo(profile)
	messages := []assistant.WebChatMessage{}
	if info.Problem == "" {
		history, err := runtime.WebChatHistory(c.Request.Context(), profile, 0)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err)
			return
		}
		messages = history
	}
	c.JSON(http.StatusOK, gin.H{"info": info, "messages": messages})
}

// webChatSend 发一句话，按行流式返回：message（每条回复）、heartbeat、done。
func (h *BotHandler) webChatSend(c *gin.Context) {
	runtime, ok := h.webChatRuntime(c)
	if !ok {
		return
	}
	var payload struct {
		Profile string `json:"profile"`
		Text    string `json:"text"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式不对"))
		return
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" || len([]rune(payload.Text)) > webChatMaxText {
		writeError(c, http.StatusBadRequest, errors.New("消息不能为空，也不能超过 4000 字"))
		return
	}
	if info := runtime.WebChatInfo(payload.Profile); info.Problem != "" {
		writeError(c, http.StatusConflict, errors.New(info.Problem))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), webChatReplyTimeout)
	defer cancel()

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	var streamMu sync.Mutex
	emit := func(value any) {
		streamMu.Lock()
		defer streamMu.Unlock()
		if err := json.NewEncoder(c.Writer).Encode(value); err != nil {
			cancel()
			return
		}
		c.Writer.Flush()
	}
	heartbeatStop, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("web_chat_heartbeat")
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit(gin.H{"type": "heartbeat"})
			}
		}
	}()
	defer func() { close(heartbeatStop); <-heartbeatDone }()

	// 回复流程在 WebChatSend 里同步跑完，每发出一条就推给页面；用户自己那句页面已经先显示了。
	replies := 0
	_, err := runtime.WebChatSend(ctx, payload.Profile, payload.Text, func(msg assistant.WebChatMessage) {
		replies++
		emit(gin.H{"type": "message", "message": msg})
	})
	if err != nil {
		text := "回复失败：" + err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			text = "回复超时了"
		}
		emit(gin.H{"type": "done", "error": text})
		return
	}
	emit(gin.H{"type": "done", "replies": replies})
}
