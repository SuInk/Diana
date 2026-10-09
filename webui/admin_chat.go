package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const adminChatMaxMessages = 40

type adminChatRuntime interface {
	RunAdminChat(context.Context, string, agent.Request, ...agent.Tool) (*agent.Response, error)
}

type adminChatMessage struct {
	ID        string              `json:"id"`
	Role      string              `json:"role"`
	Content   string              `json:"content"`
	Error     bool                `json:"error,omitempty"`
	Steps     []adminChatProgress `json:"steps,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
}

type adminChatProgress struct {
	Phase      agent.RunPhase `json:"phase"`
	Tool       string         `json:"tool,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Failed     bool           `json:"failed,omitempty"`
}

type adminChatSession struct {
	mu                 sync.Mutex
	id, owner, profile string
	title              string
	createdAt          time.Time
	messages           []adminChatMessage
	loaded             []string
	updated            time.Time
	running            bool
	cancel             context.CancelFunc
}

func (h *BotHandler) registerAdminChatRoutes(router gin.IRouter) {
	group := router.Group("/api/assistant/admin-chat", func(c *gin.Context) {
		// A group-admin token or an unchecked cookie is never admin authority.
		if !c.GetBool("webui_admin") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "需要 WebUI 管理员登录", "auth_required": true})
			return
		}
		c.Next()
	})
	group.GET("", h.getAdminChat)
	group.GET("/sessions", h.listAdminChatSessions)
	group.POST("/sessions", h.createAdminChatSession)
	group.POST("", h.runAdminChat)
	group.POST("/:id/stop", h.stopAdminChat)
	group.DELETE("/:id", h.clearAdminChat)
}

func adminChatOwner(c *gin.Context) string {
	token, _ := c.Cookie(authCookieName)
	return hashToken(token)
}

func (h *BotHandler) chatSession(c *gin.Context, id string) *adminChatSession {
	h.adminChatMu.Lock()
	defer h.adminChatMu.Unlock()
	s := h.adminChats[id]
	if s == nil || s.owner != adminChatOwner(c) {
		return nil
	}
	return s
}

func (h *BotHandler) getAdminChat(c *gin.Context) {
	profile := strings.TrimSpace(c.Query("profile"))
	if !h.validateAdminChatProfile(c, profile) {
		return
	}
	var session *adminChatSession
	if id := strings.TrimSpace(c.Query("session_id")); id != "" {
		session = h.chatSession(c, id)
		if session == nil || session.profile != profile {
			writeError(c, http.StatusNotFound, errors.New("对话不存在"))
			return
		}
	} else {
		var err error
		session, err = h.findOrCreateAdminChat(adminChatOwner(c), profile, false)
		if err != nil {
			writeError(c, http.StatusTooManyRequests, err)
			return
		}
	}
	redact := h.adminChatRedactor()
	session.mu.Lock()
	defer session.mu.Unlock()
	session.updated = time.Now()
	cfg := h.runtime.ProfileConfig(profile).WithDefaults()
	mode := assistant.NormalizeAgentMode(cfg.AgentMode)
	if mode == "" {
		mode = assistant.AgentModeStandard
	}
	if mode == assistant.AgentModeSafe {
		cfg.AgentCommandAllowlist = nil
		cfg.AgentFileWriteEnabled = false
		cfg.AgentCommandSandboxAllowNetwork = false
	}
	c.JSON(http.StatusOK, gin.H{"session_id": session.id, "profile_id": profile, "messages": session.messages, "running": session.running,
		"agent_mode": mode,
		"title":      adminChatPreview(redact(session.title), 36, "新的管理会话"), "updated_at": session.updated, "created_at": session.createdAt,
		"command_allowlist": cfg.AgentCommandAllowlist, "file_write_enabled": cfg.AgentFileWriteEnabled,
		"sandbox": cfg.AgentCommandSandbox, "network_enabled": cfg.AgentCommandSandboxAllowNetwork})
}

func (h *BotHandler) runAdminChat(c *gin.Context) {
	runtime, ok := h.runtime.(adminChatRuntime)
	if !ok {
		writeError(c, http.StatusServiceUnavailable, errors.New("当前运行时不支持管理对话"))
		return
	}
	var payload struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
		// ProviderID/Model 只覆盖这一轮管理对话的模型，留空跟随机器人。
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("对话请求格式错误或过大"))
		return
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" || utf8.RuneCountInString(payload.Message) > 12000 {
		writeError(c, http.StatusBadRequest, errors.New("消息应为 1–12000 字"))
		return
	}
	s := h.chatSession(c, payload.SessionID)
	if s == nil {
		writeError(c, http.StatusNotFound, errors.New("对话不存在，请刷新页面"))
		return
	}
	ctx, cancel := context.WithTimeout(assistant.WithAdminChatModel(c.Request.Context(), payload.ProviderID, payload.Model), 5*time.Minute)
	defer cancel()
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeError(c, http.StatusConflict, errors.New("此对话正在执行，请等待或停止"))
		return
	}
	s.running, s.cancel, s.updated = true, cancel, time.Now()
	user := adminChatMessage{ID: uuid.NewString(), Role: "user", Content: payload.Message, CreatedAt: time.Now()}
	if s.title == "" {
		s.title = payload.Message
	}
	s.messages = append(s.messages, user)
	if len(s.messages) > adminChatMaxMessages {
		s.messages = s.messages[len(s.messages)-adminChatMaxMessages:]
	}
	var history []llm.Message
	for _, m := range s.messages {
		if m.Error {
			continue
		}
		role := llm.RoleUser
		if m.Role == "assistant" {
			role = llm.RoleAssistant
		}
		history = append(history, llm.Message{Role: role, Content: m.Content})
	}
	loaded := append([]string(nil), s.loaded...)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.cancel = nil; s.updated = time.Now(); s.mu.Unlock() }()

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	// The runner's observer may be called by tools. Serialize stream writes.
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
	emit(gin.H{"type": "user", "message": user})
	heartbeatStop, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("admin_chat_heartbeat")
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
	redact := h.adminChatRedactor()
	var steps []adminChatProgress
	var stepsMu sync.Mutex
	observer := func(_ context.Context, event agent.RunEvent) {
		progress := adminChatProgress{Phase: event.Phase, Tool: event.Tool, DurationMS: event.DurationMS, Failed: event.Error != ""}
		// Do not expose raw model context, tool arguments or credential values.
		if event.Phase == agent.RunPhaseToolCompleted {
			stepsMu.Lock()
			steps = append(steps, progress)
			stepsMu.Unlock()
			recordOperation(ctx, h.logs, "admin_chat_tool", "管理对话执行工具", event.Tool, map[string]any{"profile_id": s.profile, "failed": progress.Failed, "session_id": s.id})
		}
		emit(gin.H{"type": "progress", "progress": progress})
	}
	result, err := runtime.RunAdminChat(ctx, s.profile, agent.Request{
		Messages: assistant.AdminChatMessages(history), TraceID: "admin-" + uuid.NewString(), Observer: observer, LoadedTools: loaded,
		ToolsLoaded: func(names []string) { s.mu.Lock(); s.loaded = append([]string(nil), names...); s.mu.Unlock() },
	}, &adminDiagnosticsTool{handler: h, profile: s.profile, redact: redact}, &adminGroupHistoryTool{handler: h, profile: s.profile, redact: redact})
	message := adminChatMessage{ID: uuid.NewString(), Role: "assistant", CreatedAt: time.Now()}
	stepsMu.Lock()
	message.Steps = append([]adminChatProgress(nil), steps...)
	stepsMu.Unlock()
	if err != nil {
		message.Error = true
		if errors.Is(ctx.Err(), context.Canceled) {
			message.Content = "任务已停止。已完成的操作仍然生效，可继续查询验证。"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message.Content = "本轮执行超时。已完成的操作仍然生效，可继续查询验证。"
		} else {
			message.Content = "执行失败：" + redact(err.Error())
		}
	} else if result != nil {
		message.Content = redact(result.Text)
		if message.Content == "" {
			message.Content = "本轮已结束，请查看执行记录或继续提问。"
		}
	} else {
		message.Error = true
		message.Content = "运行时未返回结果"
	}
	s.mu.Lock()
	s.messages = append(s.messages, message)
	if len(s.messages) > adminChatMaxMessages {
		s.messages = s.messages[len(s.messages)-adminChatMaxMessages:]
	}
	s.mu.Unlock()
	recordRequestOperation(c, h.logs, "admin_chat_run", "管理对话已结束", s.id, map[string]any{"profile_id": s.profile, "failed": message.Error, "tool_calls": len(message.Steps)})
	emit(gin.H{"type": "message", "message": message})
	emit(gin.H{"type": "done"})
}

func (h *BotHandler) stopAdminChat(c *gin.Context) {
	s := h.chatSession(c, c.Param("id"))
	if s == nil {
		writeError(c, http.StatusNotFound, errors.New("对话不存在"))
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *BotHandler) clearAdminChat(c *gin.Context) {
	s := h.chatSession(c, c.Param("id"))
	if s == nil {
		writeError(c, http.StatusNotFound, errors.New("对话不存在"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		writeError(c, http.StatusConflict, fmt.Errorf("请先停止正在执行的任务"))
		return
	}
	s.messages = nil
	s.loaded = nil
	s.title = ""
	s.updated = time.Now()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
