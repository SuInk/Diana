// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type relationshipEvaluationsResponse struct {
	Evaluations []assistant.RelationshipEvaluationRecord `json:"evaluations"`
	// NextBeforeID 非零时还有更早的记录，带上它再请求一次就是下一页。
	NextBeforeID int64 `json:"next_before_id,omitempty"`
}

// relationshipEvaluationStatuses 是接口认的结果分类，别的值直接丢掉，不拼进 SQL。
var relationshipEvaluationStatuses = map[string]bool{
	assistant.RelationshipEvaluationChanged:       true,
	assistant.RelationshipEvaluationCapped:        true,
	assistant.RelationshipEvaluationUnchanged:     true,
	assistant.RelationshipEvaluationLowConfidence: true,
	assistant.RelationshipEvaluationFailed:        true,
	assistant.RelationshipEvaluationSkipped:       true,
}

// listRelationshipEvaluations 列出后台好感度评估记录。status 用逗号分隔多个结果，
// 留空表示全部；changed=1 只要分数变了或记下了画像的，portrait=1 只要记下了画像的；
// q 按 QQ 号或昵称模糊找人，group_id 按群精确筛。
func (h *BotHandler) listRelationshipEvaluations(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "人员画像存储未配置"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	beforeID, _ := strconv.ParseInt(c.Query("before_id"), 10, 64)
	var statuses []string
	for _, status := range strings.Split(c.Query("status"), ",") {
		if status = strings.TrimSpace(status); relationshipEvaluationStatuses[status] {
			statuses = append(statuses, status)
		}
	}
	// 多取一条判断还有没有下一页，省一次计数查询。
	records, err := h.sqlite.ListRelationshipEvaluations(c.Request.Context(), assistant.RelationshipEvaluationFilter{
		BotProfileID: botProfileScope(c),
		UserID:       strings.TrimSpace(c.Query("user_id")),
		GroupID:      strings.TrimSpace(c.Query("group_id")),
		Query:        strings.TrimSpace(c.Query("q")),
		Statuses:     statuses,
		ChangedOnly:  c.Query("changed") == "1",
		HasPortrait:  c.Query("portrait") == "1",
		BeforeID:     beforeID,
		Limit:        limit + 1,
	})
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "relationship_evaluations_list", err, "", nil)
		return
	}
	response := relationshipEvaluationsResponse{Evaluations: records}
	if len(records) > limit {
		response.Evaluations = records[:limit]
		response.NextBeforeID = records[limit-1].ID
	}
	c.JSON(http.StatusOK, response)
}
