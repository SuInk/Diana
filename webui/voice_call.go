// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/voicecall"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 实时语音通话的接入点。网页测试页和以后的手机 App 走同一个 WebSocket，协议见
// docs/voice-call.md，会话逻辑在 model/voicecall。这里只管鉴权、升级连接和收发帧。
const (
	voiceCallWriteTimeout = 10 * time.Second
	voiceCallPingInterval = 20 * time.Second
	voiceCallReadTimeout  = 60 * time.Second
	// 一帧上行最多 1 MiB：PCM 一般 20–100ms 一帧，整句上传的压缩音频也够用。
	voiceCallMaxFrame = 1 << 20
)

type voiceCallRuntime interface {
	VoiceCallInfo(context.Context, string) assistant.VoiceCallInfo
	NewVoiceCall(context.Context, string, string) (*assistant.VoiceCall, error)
}

var voiceCallUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 64 << 10,
	// 端点在 /api 下，已经过了 WebUI 的会话鉴权；这里只要求同源，不让跨站页面
	// 借着登录 Cookie 连进来。
	CheckOrigin: sameOriginWebSocket,
}

func (h *BotHandler) registerVoiceCallRoutes(router gin.IRouter) {
	group := router.Group("/api/assistant/call", func(c *gin.Context) {
		if !c.GetBool("webui_admin") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "需要 WebUI 管理员登录", "auth_required": true})
			return
		}
		c.Next()
	})
	group.GET("", h.voiceCallInfo)
	group.GET("/ws", h.voiceCallSocket)
}

func (h *BotHandler) voiceCallRuntime(c *gin.Context) (voiceCallRuntime, bool) {
	runtime, ok := h.runtime.(voiceCallRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "运行时不支持语音通话"})
	}
	return runtime, ok
}

// voiceCallInfo 告诉前端这台机器人现在能不能打电话、缺什么。
func (h *BotHandler) voiceCallInfo(c *gin.Context) {
	runtime, ok := h.voiceCallRuntime(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, runtime.VoiceCallInfo(c.Request.Context(), strings.TrimSpace(c.Query("profile"))))
}

func (h *BotHandler) voiceCallSocket(c *gin.Context) {
	runtime, ok := h.voiceCallRuntime(c)
	if !ok {
		return
	}
	profile := strings.TrimSpace(c.Query("profile"))
	callID := newVoiceCallID()
	// 先建好机器人那一侧再升级：插槽没配、没有主人账号时直接回 HTTP 错误，
	// 客户端不用连上再断开才知道原因。
	call, err := runtime.NewVoiceCall(c.Request.Context(), profile, callID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	info := runtime.VoiceCallInfo(c.Request.Context(), profile)
	conn, err := voiceCallUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(voiceCallMaxFrame)

	// 通话不跟着 HTTP 请求的 ctx 走：升级之后那个 ctx 的生命周期由 gin 决定。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &voiceCallConn{conn: conn}
	session := voicecall.NewSession(ctx, call, out, voicecall.Options{CallID: callID, BotID: info.BotID, BotName: info.BotName})
	defer session.Close()
	recordOperation(ctx, h.logs, "voice_call", "开始语音通话", info.BotID, map[string]any{"call_id": callID})
	started := time.Now()
	defer func() {
		recordOperation(context.Background(), h.logs, "voice_call", "结束语音通话", info.BotID, map[string]any{"call_id": callID, "duration_seconds": int(time.Since(started).Seconds())})
	}()

	go func() {
		defer recoverGoroutinePanic("voice_call.keepalive")
		out.keepAlive(ctx)
	}()
	_ = conn.SetReadDeadline(time.Now().Add(voiceCallReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(voiceCallReadTimeout))
	})
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(voiceCallReadTimeout))
		switch kind {
		case websocket.BinaryMessage:
			session.HandleAudio(data)
		case websocket.TextMessage:
			var msg voicecall.ClientMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				_ = out.WriteJSON(voicecall.ServerMessage{Type: voicecall.ServerError, Code: "bad_message", Text: "控制消息不是合法的 JSON"})
				continue
			}
			if err := session.HandleControl(msg); errors.Is(err, voicecall.ErrHangup) {
				_ = out.close(websocket.CloseNormalClosure, "hangup")
				return
			}
		}
	}
}

// voiceCallConn 串行写 WebSocket：会话和保活协程都会写。
type voiceCallConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *voiceCallConn) WriteJSON(msg voicecall.ServerMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.write(websocket.TextMessage, body)
}

func (c *voiceCallConn) WriteBinary(data []byte) error {
	return c.write(websocket.BinaryMessage, data)
}

func (c *voiceCallConn) write(kind int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(voiceCallWriteTimeout))
	return c.conn.WriteMessage(kind, data)
}

func (c *voiceCallConn) close(code int, reason string) error {
	return c.write(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
}

func (c *voiceCallConn) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(voiceCallPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.write(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}
}

func newVoiceCallID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
