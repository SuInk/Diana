package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type adminChatSummary struct {
	ID           string    `json:"session_id"`
	Profile      string    `json:"profile_id"`
	Title        string    `json:"title"`
	Preview      string    `json:"preview"`
	UpdatedAt    time.Time `json:"updated_at"`
	CreatedAt    time.Time `json:"created_at"`
	Running      bool      `json:"running"`
	MessageCount int       `json:"message_count"`
}

func adminChatPreview(text string, limit int, fallback string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return fallback
	}
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

func (h *BotHandler) validateAdminChatProfile(c *gin.Context, profile string) bool {
	if _, ok := h.runtime.(adminChatRuntime); !ok {
		writeError(c, http.StatusServiceUnavailable, errors.New("当前运行时不支持管理对话"))
		return false
	}
	if profile == "" {
		return true
	}
	for _, cfg := range h.runtime.ProfileConfigs() {
		if cfg.ID == profile {
			return true
		}
	}
	writeError(c, http.StatusNotFound, errors.New("机器人不存在"))
	return false
}

// latestOrNewAdminChat 打开管理对话时接着最近的那条会话；这个机器人还没有会话就新建一条。
func (h *BotHandler) latestOrNewAdminChat(ctx context.Context, store *storage.SQLiteStore, profile string) (storage.AdminChatSession, error) {
	sessions, err := store.ListAdminChatSessions(ctx, profile)
	if err != nil || len(sessions) > 0 {
		if err != nil {
			return storage.AdminChatSession{}, err
		}
		return sessions[0], nil
	}
	return h.newAdminChat(ctx, store, profile)
}

func (h *BotHandler) newAdminChat(ctx context.Context, store *storage.SQLiteStore, profile string) (storage.AdminChatSession, error) {
	now := time.Now()
	session := storage.AdminChatSession{ID: uuid.NewString(), ProfileID: profile, CreatedAt: now, UpdatedAt: now}
	return session, store.CreateAdminChatSession(ctx, session.ID, profile, now)
}

func (h *BotHandler) listAdminChatSessions(c *gin.Context) {
	profile := strings.TrimSpace(c.Query("profile"))
	if !h.validateAdminChatProfile(c, profile) {
		return
	}
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	sessions, err := store.ListAdminChatSessions(c.Request.Context(), profile)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	redact := h.adminChatRedactor()
	items := make([]adminChatSummary, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, adminChatSummary{ID: s.ID, Profile: s.ProfileID, Title: adminChatPreview(redact(s.Title), 36, "新的管理会话"), Preview: adminChatPreview(redact(s.LastContent), 100, "还没有消息"), UpdatedAt: s.UpdatedAt, CreatedAt: s.CreatedAt, Running: h.adminChatRunning(s.ID), MessageCount: s.MessageCount})
	}
	c.JSON(http.StatusOK, gin.H{"sessions": items})
}

func (h *BotHandler) createAdminChatSession(c *gin.Context) {
	var payload struct {
		Profile string `json:"profile"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("会话请求格式无效"))
		return
	}
	profile := strings.TrimSpace(payload.Profile)
	if !h.validateAdminChatProfile(c, profile) {
		return
	}
	store := h.adminChatStore(c)
	if store == nil {
		return
	}
	s, err := h.newAdminChat(c.Request.Context(), store, profile)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"session_id": s.ID, "profile_id": s.ProfileID})
}
