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
	// MemoryCount 数的是原始发言缓冲（profile.Memories），不是长期记忆。真正的
	// 长期记忆条数在 StructuredMemoryCount 里。
	MemoryCount           int `json:"memory_count"`
	StructuredMemoryCount int `json:"structured_memory_count"`
	PortraitCount         int `json:"portrait_count"`
}

type assistantUsersResponse struct {
	Users  []assistantUserSummary `json:"users"`
	Total  int                    `json:"total"`
	Query  string                 `json:"query,omitempty"`
	Sort   string                 `json:"sort"`
	Order  string                 `json:"order"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

type assistantUserDetailResponse struct {
	Profile             assistant.UserMemoryProfile        `json:"profile"`
	FavorabilityChanges []assistant.UserFavorabilityChange `json:"favorability_changes"`
	// PortraitFields 是画像的栏目表，控制台按它排版并显示空栏，不必自己再抄一份
	// 字段到中文的映射。
	PortraitFields []assistant.PortraitFieldSpec `json:"portrait_fields"`
	// StructuredMemories 才是门控器写出来的长期记忆。Profile.Memories 是原始发言
	// 的环形缓冲，只留给排查用，控制台按「最近发言」显示。
	StructuredMemories []assistant.StructuredMemoryItem `json:"structured_memories"`
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
	// 排序参数先收敛再用，非法值当默认排序处理，不给前端报错。
	sort, order := storage.NormalizeUserMemorySort(c.Query("sort"), c.Query("order"))
	profiles, total, err := h.sqlite.ListUserMemoriesSorted(c.Request.Context(), botProfileScope(c), query, sort, order, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	userIDs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		userIDs = append(userIDs, profile.UserID)
	}
	// 一次数完整页人的长期记忆条数：逐个 COUNT 会把一页 50 人变成 50 次查询。
	// 数不出来不算致命，列表照常显示，长期记忆一栏显示 0。
	memoryCounts, err := h.sqlite.CountStructuredMemoriesBySubjects(c.Request.Context(), userIDs)
	if err != nil {
		memoryCounts = map[string]int{}
	}
	users := make([]assistantUserSummary, 0, len(profiles))
	for _, profile := range profiles {
		summary := assistantUserSummary{
			UserMemoryProfile:     profile,
			MemoryCount:           len(profile.Memories),
			StructuredMemoryCount: memoryCounts[profile.UserID],
			PortraitCount:         len(profile.Portrait),
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
		Sort:   sort,
		Order:  order,
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
	// 长期记忆不跟着机器人分身走（memory_items 没有 bot_profile_id 列），所以这里
	// 不传作用域。
	memories, err := h.sqlite.ListStructuredMemoriesBySubject(c.Request.Context(), userID, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if memories == nil {
		memories = []assistant.StructuredMemoryItem{}
	}
	c.JSON(http.StatusOK, assistantUserDetailResponse{
		Profile:             profile,
		FavorabilityChanges: changes,
		PortraitFields:      assistant.PortraitFieldSpecs(),
		StructuredMemories:  memories,
	})
}

// botProfileScope 读控制台传来的机器人作用域。留空表示「全部机器人」。
func botProfileScope(c *gin.Context) string {
	return strings.TrimSpace(c.Query("profile"))
}
