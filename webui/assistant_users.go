// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type assistantUserSummary struct {
	assistant.UserMemoryProfile
	MemoryCount   int `json:"memory_count"`
	PortraitCount int `json:"portrait_count"`
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
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "人员不存在或还没有画像记录"})
		return
	}
	changes, err := h.sqlite.ListUserFavorabilityChanges(c.Request.Context(), botProfileScope(c), userID, 50)
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
