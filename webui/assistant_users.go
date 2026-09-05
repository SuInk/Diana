// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

func (h *BotHandler) editAssistantUser(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "人员画像存储未配置"})
		return
	}
	var payload struct {
		Profile assistant.UserMemoryProfile `json:"profile"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Profile.UpdatedAt.IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少有效的人员记录版本"})
		return
	}
	p := payload.Profile
	if scope, supplied := c.GetQuery("profile"); !supplied || strings.TrimSpace(scope) != p.BotProfileID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定人员记录所属机器人"})
		return
	}
	remove := c.Request.Method == http.MethodDelete
	if !remove {
		if p.Favorability < -100 || p.Favorability > 200 || len([]rune(p.DisplayName)) > 200 || len(p.Memories) > 20 || len(p.Portrait) > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "昵称、好感度或记忆条数超出限制"})
			return
		}
		for _, item := range p.Memories {
			if strings.TrimSpace(item.Text) == "" || len([]rune(item.Text)) > 1000 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "记忆内容不能为空且最多 1000 字"})
				return
			}
		}
		for _, trait := range p.Portrait {
			if _, ok := assistant.NormalizePortraitField(string(trait.Field)); !ok || strings.TrimSpace(trait.Value) == "" || len([]rune(trait.Value)) > 1000 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "画像栏目或内容无效"})
				return
			}
		}
	}
	err := h.sqlite.EditUserMemory(c.Request.Context(), p.BotProfileID, strings.TrimSpace(c.Param("id")), p, remove)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrUserMemoryConflict) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	action, message := "assistant.users.save", "人员记录已修改"
	if remove {
		action, message = "assistant.users.delete", "人员记录已删除"
	}
	recordRequestOperation(c, h.logs, action, message, c.Param("id"), map[string]any{"bot_profile_id": p.BotProfileID})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type assistantUserSummary struct {
	assistant.UserMemoryProfile
	MemoryCount   int `json:"memory_count"`
	PortraitCount int `json:"portrait_count"`
}

type assistantUsersResponse struct {
	Users  []assistantUserSummary `json:"users"`
	Total  int                    `json:"total"`
	Query  string                 `json:"query,omitempty"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

type assistantUserDetailResponse struct {
	Profile             assistant.UserMemoryProfile        `json:"profile"`
	FavorabilityChanges []assistant.UserFavorabilityChange `json:"favorability_changes"`
	// PortraitFields 是画像的栏目表，控制台按它排版并显示空栏，不必自己再抄一份
	// 字段到中文的映射。
	PortraitFields []assistant.PortraitFieldSpec `json:"portrait_fields"`
}

// listAssistantUsers 返回机器人记住的人员画像列表，供控制台人员管理使用。
func (h *BotHandler) listAssistantUsers(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "人员画像存储未配置"})
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	profiles, total, err := h.sqlite.ListUserMemories(c.Request.Context(), botProfileScope(c), query, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	users := make([]assistantUserSummary, 0, len(profiles))
	for _, profile := range profiles {
		summary := assistantUserSummary{
			UserMemoryProfile: profile,
			MemoryCount:       len(profile.Memories),
			PortraitCount:     len(profile.Portrait),
		}
		// 列表只要条数，正文放在详情接口，避免人员多时响应过大。
		summary.Memories = nil
		summary.Portrait = nil
		users = append(users, summary)
	}
	c.JSON(http.StatusOK, assistantUsersResponse{
		Users:  users,
		Total:  total,
		Query:  query,
		Limit:  limit,
		Offset: offset,
	})
}

// getAssistantUser 返回单个人员的长期记忆与好感度变更历史。
func (h *BotHandler) getAssistantUser(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "人员画像存储未配置"})
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	profile, found, err := h.sqlite.GetUserMemory(c.Request.Context(), botProfileScope(c), userID)
	if _, supplied := c.GetQuery("profile"); supplied {
		profile, found, err = h.sqlite.GetUserMemoryExact(c.Request.Context(), botProfileScope(c), userID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "人员不存在或还没有画像记录"})
		return
	}
	changes, err := h.sqlite.ListUserFavorabilityChangesExact(c.Request.Context(), profile.BotProfileID, userID, 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if changes == nil {
		changes = []assistant.UserFavorabilityChange{}
	}
	if profile.Memories == nil {
		profile.Memories = []assistant.UserMemoryItem{}
	}
	if profile.Portrait == nil {
		profile.Portrait = []assistant.UserPortraitTrait{}
	}
	c.JSON(http.StatusOK, assistantUserDetailResponse{
		Profile:             profile,
		FavorabilityChanges: changes,
		PortraitFields:      assistant.PortraitFieldSpecs(),
	})
}

// botProfileScope 读控制台传来的机器人作用域。留空表示「全部机器人」。
func botProfileScope(c *gin.Context) string {
	return strings.TrimSpace(c.Query("profile"))
}
