package webui

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

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

func (h *BotHandler) findOrCreateAdminChat(owner, profile string, newConversation bool) (*adminChatSession, error) {
	h.adminChatMu.Lock()
	defer h.adminChatMu.Unlock()
	if h.adminChats == nil {
		h.adminChats = map[string]*adminChatSession{}
	}
	var latest *adminChatSession
	var latestAt time.Time
	if !newConversation {
		for _, s := range h.adminChats {
			if s.owner != owner || s.profile != profile {
				continue
			}
			s.mu.Lock()
			updated := s.updated
			s.mu.Unlock()
			if latest == nil || updated.After(latestAt) {
				latest, latestAt = s, updated
			}
		}
		if latest != nil {
			return latest, nil
		}
	}
	// Bound idle state; active runs are never evicted.
	for id, s := range h.adminChats {
		s.mu.Lock()
		idle := !s.running && time.Since(s.updated) > 30*time.Minute
		s.mu.Unlock()
		if idle {
			delete(h.adminChats, id)
		}
	}
	if len(h.adminChats) >= 64 {
		return nil, errors.New("管理对话会话过多，请稍后重试")
	}
	now := time.Now()
	s := &adminChatSession{id: uuid.NewString(), owner: owner, profile: profile, updated: now, createdAt: now}
	h.adminChats[s.id] = s
	return s, nil
}

func (h *BotHandler) listAdminChatSessions(c *gin.Context) {
	profile := strings.TrimSpace(c.Query("profile"))
	if !h.validateAdminChatProfile(c, profile) {
		return
	}
	redact := h.adminChatRedactor()
	owner := adminChatOwner(c)
	items := make([]adminChatSummary, 0)
	h.adminChatMu.Lock()
	for _, s := range h.adminChats {
		if s.owner != owner || s.profile != profile {
			continue
		}
		s.mu.Lock()
		preview := ""
		if len(s.messages) > 0 {
			preview = s.messages[len(s.messages)-1].Content
		}
		items = append(items, adminChatSummary{ID: s.id, Profile: s.profile, Title: adminChatPreview(redact(s.title), 36, "新的管理会话"), Preview: adminChatPreview(redact(preview), 100, "还没有消息"), UpdatedAt: s.updated, CreatedAt: s.createdAt, Running: s.running, MessageCount: len(s.messages)})
		s.mu.Unlock()
	}
	h.adminChatMu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
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
	s, err := h.findOrCreateAdminChat(adminChatOwner(c), profile, true)
	if err != nil {
		writeError(c, http.StatusTooManyRequests, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"session_id": s.id, "profile_id": s.profile})
}
