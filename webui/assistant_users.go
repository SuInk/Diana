// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// portraitTraitRejection 把「这一栏为什么不收」说成能照着改的一句话。
func portraitTraitRejection(trait assistant.UserPortraitTrait) string {
	field, ok := assistant.NormalizePortraitField(string(trait.Field))
	if !ok {
		return "画像栏目或内容无效"
	}
	if field == assistant.PortraitFieldTimezone {
		return "时区要填 IANA 时区名，例如 Asia/Shanghai、Europe/Berlin；「在德国」「比我慢六小时」这类描述换算不了时间，不能收"
	}
	return "「" + assistant.PortraitFieldLabel(field) + "」这一栏的内容无效"
}

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
		// 手填的画像要走和模型写入同一道归一：补栏目名、收紧空白、按栏校验取值。
		// 时区那一栏尤其不能放过——存进一句「在德国」不会报错，但它换算不出时间，
		// 跨时区那条链路只会当成「没记过」，人还以为自己已经标上了。
		normalized := make([]assistant.UserPortraitTrait, 0, len(p.Portrait))
		for _, trait := range p.Portrait {
			if _, ok := assistant.NormalizePortraitField(string(trait.Field)); !ok || strings.TrimSpace(trait.Value) == "" || len([]rune(trait.Value)) > 1000 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "画像栏目或内容无效"})
				return
			}
			clean, ok := assistant.NormalizePortraitTrait(trait, time.Now())
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": portraitTraitRejection(trait)})
				return
			}
			normalized = append(normalized, clean)
		}
		p.Portrait = normalized
	}
	err := h.sqlite.EditUserMemory(c.Request.Context(), p.BotProfileID, strings.TrimSpace(c.Param("id")), p, remove)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrUserMemoryConflict) {
			status = http.StatusConflict
		}
		h.writeError(c, status, "users_save", err, c.Param("id"), map[string]any{"bot_profile_id": p.BotProfileID, "remove": remove})
		return
	}
	action, message := "users_save", "人员记录已修改"
	if remove {
		action, message = "users_delete", "人员记录已删除"
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
		h.writeError(c, http.StatusInternalServerError, "users_list", err, "", nil)
		return
	}
	userIDsByProfile := make(map[string][]string)
	for _, profile := range profiles {
		userIDsByProfile[profile.BotProfileID] = append(userIDsByProfile[profile.BotProfileID], profile.UserID)
	}
	// 按机器人批量统计；相同账号在不同机器人下不能共用计数。
	memoryCounts := make(map[string]map[string]int)
	for profileID, userIDs := range userIDsByProfile {
		counts, err := h.sqlite.CountStructuredMemoriesBySubjects(c.Request.Context(), profileID, userIDs)
		if err == nil {
			memoryCounts[profileID] = counts
		}
	}
	users := make([]assistantUserSummary, 0, len(profiles))
	for _, profile := range profiles {
		summary := assistantUserSummary{
			UserMemoryProfile:     profile,
			MemoryCount:           len(profile.Memories),
			StructuredMemoryCount: memoryCounts[profile.BotProfileID][profile.UserID],
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
		h.writeError(c, http.StatusInternalServerError, "users_get", err, userID, nil)
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "人员不存在或还没有画像记录"})
		return
	}
	changes, err := h.sqlite.ListUserFavorabilityChangesExact(c.Request.Context(), profile.BotProfileID, userID, 50)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "users_get", err, userID, nil)
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
	memories, err := h.sqlite.ListStructuredMemoriesBySubject(c.Request.Context(), profile.BotProfileID, userID, 100)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "users_get", err, userID, nil)
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

// clearAssistantUserMemories 清空一个人身上的长期记忆，或只清其中一条。
//
// 人员记录本身不动：好感度、画像和原始发言缓冲都留着。要连人一起删走
// DELETE /users/:id。
func (h *BotHandler) clearAssistantUserMemories(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "人员画像存储未配置"})
		return
	}
	// 必须显式指定机器人作用域：留空在这里不是「全部机器人」而是「只匹配没有
	// 命名空间的旧记录」，清空这种不可逆的操作不能靠猜。
	scope, supplied := c.GetQuery("profile")
	if !supplied || strings.TrimSpace(scope) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定记忆所属机器人"})
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	memoryID := strings.TrimSpace(c.Param("memory"))
	cleared, err := h.sqlite.ForgetStructuredMemoriesBySubject(c.Request.Context(), strings.TrimSpace(scope), userID, memoryID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "users_memories_clear", err, c.Param("id"), nil)
		return
	}
	message := "长期记忆已清空"
	if memoryID != "" {
		message = "单条长期记忆已删除"
	}
	recordRequestOperation(c, h.logs, "user_memories_clear", message, userID, map[string]any{
		"bot_profile_id": strings.TrimSpace(scope),
		"memory_id":      memoryID,
		"cleared":        cleared,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true, "cleared": cleared})
}

// botProfileScope 读控制台传来的机器人作用域。留空表示「全部机器人」。
func botProfileScope(c *gin.Context) string {
	return strings.TrimSpace(c.Query("profile"))
}
