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
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

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

// adminChatProgress 是一步执行记录。Summary 只取路径、命令、网址、搜索词这类白名单参数，
// 脱敏后给人看「它在干什么」；原始参数和工具输出不出去。
type adminChatProgress struct {
	Phase      agent.RunPhase `json:"phase"`
	Tool       string         `json:"tool,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Failed     bool           `json:"failed,omitempty"`
	Error      string         `json:"error,omitempty"`
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
	group.DELETE("/sessions/:id", h.deleteAdminChatSession)
	group.POST("", h.runAdminChat)
	group.POST("/:id/stop", h.stopAdminChat)
	group.DELETE("/:id", h.clearAdminChat)
}

// adminChatStore 取会话存储；没有数据库（只在测试桩里）时直接报错，不再退回内存。
func (h *BotHandler) adminChatStore(c *gin.Context) *storage.SQLiteStore {
	if h.sqlite == nil {
		writeError(c, http.StatusServiceUnavailable, errors.New("管理对话需要数据库"))
	}
	return h.sqlite
}

// adminChatLoad 读一条会话，不存在时写 404。
func (h *BotHandler) adminChatLoad(c *gin.Context, store *storage.SQLiteStore, id string) (storage.AdminChatSession, bool) {
	session, err := store.GetAdminChatSession(c.Request.Context(), id)
	if errors.Is(err, storage.ErrAdminChatSessionNotFound) {
		writeError(c, http.StatusNotFound, errors.New("对话不存在"))
		return session, false
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return session, false
	}
	return session, true
}

// adminChatRunning 报告会话是否正在执行。执行状态只在内存里：服务重启后没有在跑的任务。
func (h *BotHandler) adminChatRunning(id string) bool {
	h.adminChatMu.Lock()
	defer h.adminChatMu.Unlock()
	return h.adminChatRuns[id] != nil
}

func decodeAdminChatMessages(stored []storage.AdminChatMessage, redact func(string) string) []adminChatMessage {
	out := make([]adminChatMessage, 0, len(stored))
	for _, m := range stored {
		message := adminChatMessage{ID: m.ID, Role: m.Role, Content: redact(m.Content), Error: m.Error, CreatedAt: m.CreatedAt}
		if m.Steps != "" {
			_ = json.Unmarshal([]byte(m.Steps), &message.Steps)
		}
		out = append(out, message)
	}
	return out
}

func (h *BotHandler) getAdminChat(c *gin.Context) {
	profile := strings.TrimSpace(c.Query("profile"))
	if !h.validateAdminChatProfile(c, profile) {
		return
	}
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	var session storage.AdminChatSession
	if id := strings.TrimSpace(c.Query("session_id")); id != "" {
		var ok bool
		if session, ok = h.adminChatLoad(c, store, id); !ok {
			return
		}
		if session.ProfileID != profile {
			writeError(c, http.StatusNotFound, errors.New("对话不存在"))
			return
		}
	} else {
		var err error
		if session, err = h.latestOrNewAdminChat(c.Request.Context(), store, profile); err != nil {
			writeError(c, http.StatusInternalServerError, err)
			return
		}
	}
	stored, err := store.AdminChatMessages(c.Request.Context(), session.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	redact := h.adminChatRedactor()
	cfg := h.runtime.ProfileConfig(profile).WithDefaults()
	mode := assistant.NormalizeAgentMode(cfg.AgentMode)
	if mode == "" {
		mode = assistant.AgentModeStandard
	}
	if mode == assistant.AgentModeSafe {
		cfg.AgentFileWriteEnabled = false
		cfg.AgentCommandSandboxAllowNetwork = false
	}
	c.JSON(http.StatusOK, gin.H{"session_id": session.ID, "profile_id": profile, "messages": decodeAdminChatMessages(stored, redact), "running": h.adminChatRunning(session.ID),
		"agent_mode": mode,
		"title":      adminChatPreview(redact(session.Title), 36, "新的管理会话"), "updated_at": session.UpdatedAt, "created_at": session.CreatedAt,
		"command_enabled": mode != assistant.AgentModeSafe && assistant.NormalizeBoxAccess(cfg.AgentBoxAccess) != assistant.BoxAccessOff && boxStatusOK(), "file_write_enabled": cfg.AgentFileWriteEnabled,
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
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	s, found := h.adminChatLoad(c, store, payload.SessionID)
	if !found {
		return
	}
	// 不设总时长：长任务跑到完，管理员随时可以停。连接断开（关页面、桌面端退出）时随请求一起取消。
	ctx, cancel := context.WithCancel(assistant.WithAdminChatSession(assistant.WithAdminChatModel(c.Request.Context(), payload.ProviderID, payload.Model), s.ID))
	defer cancel()
	h.adminChatMu.Lock()
	if h.adminChatRuns[s.ID] != nil {
		h.adminChatMu.Unlock()
		writeError(c, http.StatusConflict, errors.New("此对话正在执行，请等待或停止"))
		return
	}
	if h.adminChatRuns == nil {
		h.adminChatRuns = map[string]context.CancelFunc{}
	}
	h.adminChatRuns[s.ID] = cancel
	h.adminChatMu.Unlock()
	defer func() { h.adminChatMu.Lock(); delete(h.adminChatRuns, s.ID); h.adminChatMu.Unlock() }()

	// 落库用不随请求取消的 context：停止或断开之后，「已停止」那条也要记下来。
	persist := context.WithoutCancel(ctx)
	user := adminChatMessage{ID: uuid.NewString(), Role: "user", Content: payload.Message, CreatedAt: time.Now()}
	if err := store.AppendAdminChatMessage(persist, s.ID, storage.AdminChatMessage{ID: user.ID, Role: user.Role, Content: user.Content, CreatedAt: user.CreatedAt}); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	stored, err := store.AdminChatMessages(persist, s.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	var history []llm.Message
	for _, m := range stored {
		if m.Error {
			continue
		}
		role := llm.RoleUser
		if m.Role == "assistant" {
			role = llm.RoleAssistant
		}
		history = append(history, llm.Message{Role: role, Content: m.Content})
	}
	var loaded []string
	if s.LoadedTools != "" {
		loaded = strings.Split(s.LoadedTools, ",")
	}

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
	redact := h.adminChatRedactor()
	publicUser := user
	publicUser.Content = redact(user.Content)
	emit(gin.H{"type": "user", "message": publicUser})
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
	var steps []adminChatProgress
	var stepsMu sync.Mutex
	observer := func(_ context.Context, event agent.RunEvent) {
		progress := adminChatProgress{Phase: event.Phase, Tool: event.Tool, DurationMS: event.DurationMS, Failed: event.Error != ""}
		if event.Phase == agent.RunPhaseToolStarted || event.Phase == agent.RunPhaseToolCompleted {
			progress.Summary = adminChatToolSummary(event.ToolInput, redact)
		}
		if event.Phase == agent.RunPhaseToolCompleted && event.Error != "" {
			progress.Error = adminChatClip(redact(event.Error), 300)
		}
		// Do not expose raw model context, tool arguments or credential values.
		if event.Phase == agent.RunPhaseToolCompleted {
			stepsMu.Lock()
			steps = append(steps, progress)
			stepsMu.Unlock()
			recordOperation(ctx, h.logs, "admin_chat_tool", "管理对话执行工具", event.Tool, map[string]any{"profile_id": s.ProfileID, "failed": progress.Failed, "session_id": s.ID})
		}
		emit(gin.H{"type": "progress", "progress": progress})
	}
	result, err := runtime.RunAdminChat(ctx, s.ProfileID, agent.Request{
		Messages: assistant.AdminChatMessages(history), TraceID: "admin-" + uuid.NewString(), Observer: observer, LoadedTools: loaded,
		ToolsLoaded: func(names []string) { _ = store.SetAdminChatLoadedTools(persist, s.ID, strings.Join(names, ",")) },
	}, &adminDiagnosticsTool{handler: h, profile: s.ProfileID, redact: redact}, &adminGroupHistoryTool{handler: h, profile: s.ProfileID, redact: redact})
	message := adminChatMessage{ID: uuid.NewString(), Role: "assistant", CreatedAt: time.Now()}
	stepsMu.Lock()
	message.Steps = append([]adminChatProgress(nil), steps...)
	stepsMu.Unlock()
	if err != nil {
		message.Error = true
		if errors.Is(ctx.Err(), context.Canceled) {
			message.Content = "任务已停止。已完成的操作仍然生效，可继续查询验证。"
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
	stepsJSON := ""
	if len(message.Steps) > 0 {
		raw, _ := json.Marshal(message.Steps)
		stepsJSON = string(raw)
	}
	if err := store.AppendAdminChatMessage(persist, s.ID, storage.AdminChatMessage{ID: message.ID, Role: message.Role, Content: message.Content, Error: message.Error, Steps: stepsJSON, CreatedAt: message.CreatedAt}); err != nil {
		message.Error = true
		message.Content += "\n\n（这条回复没能保存：" + err.Error() + "）"
	}
	recordRequestOperation(c, h.logs, "admin_chat_run", "管理对话已结束", s.ID, map[string]any{"profile_id": s.ProfileID, "failed": message.Error, "tool_calls": len(message.Steps)})
	emit(gin.H{"type": "message", "message": message})
	emit(gin.H{"type": "done"})
}

// adminChatSummaryKeys 是步骤摘要能用的参数：都是「做了什么」的说明，不含凭据。
var adminChatSummaryKeys = []string{"command", "cmd", "path", "file", "dir", "url", "query", "q", "pattern", "name", "title", "action"}

func adminChatToolSummary(input map[string]any, redact func(string) string) string {
	var parts []string
	for _, key := range adminChatSummaryKeys {
		value, ok := input[key].(string)
		if !ok {
			continue
		}
		if value = strings.Join(strings.Fields(value), " "); value != "" {
			parts = append(parts, adminChatClip(redact(value), 160))
		}
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// adminChatClip 只用于界面上的一行摘要，不进模型上下文。
func adminChatClip(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

func (h *BotHandler) stopAdminChat(c *gin.Context) {
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	if _, ok := h.adminChatLoad(c, store, c.Param("id")); !ok {
		return
	}
	h.adminChatMu.Lock()
	if cancel := h.adminChatRuns[c.Param("id")]; cancel != nil {
		cancel()
	}
	h.adminChatMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *BotHandler) clearAdminChat(c *gin.Context) {
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	if _, ok := h.adminChatLoad(c, store, c.Param("id")); !ok {
		return
	}
	if h.adminChatRunning(c.Param("id")) {
		writeError(c, http.StatusConflict, fmt.Errorf("请先停止正在执行的任务"))
		return
	}
	if err := store.ClearAdminChatSession(c.Request.Context(), c.Param("id"), time.Now()); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *BotHandler) deleteAdminChatSession(c *gin.Context) {
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	if _, ok := h.adminChatLoad(c, store, c.Param("id")); !ok {
		return
	}
	if h.adminChatRunning(c.Param("id")) {
		writeError(c, http.StatusConflict, fmt.Errorf("请先停止正在执行的任务"))
		return
	}
	if err := store.DeleteAdminChatSession(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func boxStatusOK() bool {
	ok, _ := agent.BoxStatus()
	return ok
}
