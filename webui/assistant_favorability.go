// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type relationshipEvaluationsResponse struct {
	Evaluations []assistant.RelationshipEvaluationRecord `json:"evaluations"`
	// PortraitFields 是画像栏目表，高级筛选按它列可选栏目，不在前端再抄一份。
	PortraitFields []assistant.PortraitFieldSpec `json:"portrait_fields"`
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

// allowedValue 只放行白名单里的取值，别的一律当不限。
func allowedValue(value string, allowed ...string) string {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return ""
}

// listRelationshipEvaluations 列出后台好感度评估记录。status 用逗号分隔多个结果，
// 留空表示全部；portrait=1 只要记下了画像的；q 什么都搜；person 按 QQ 号或昵称
// 模糊找人；group_id 按群精确筛；since 是 Unix 秒，只要这之后的；direction 是
// up/down/changed/none；chat 是 group/private；portrait_field 逗号分隔栏目；
// portrait_source 是 stated/inferred；min_confidence 是 0 到 1；model 模糊匹配。
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
	var since time.Time
	if seconds, err := strconv.ParseInt(c.Query("since"), 10, 64); err == nil && seconds > 0 {
		since = time.Unix(seconds, 0)
	}
	var minConfidence float64
	if value, err := strconv.ParseFloat(c.Query("min_confidence"), 64); err == nil && value > 0 && value <= 1 {
		minConfidence = value
	}
	knownFields := map[string]bool{}
	for _, spec := range assistant.PortraitFieldSpecs() {
		knownFields[string(spec.Field)] = true
	}
	// 多选项「没传」是不限，「传了空值」是一个都不要：页面上是默认全选、取消哪个
	// 就不看哪个，全取消就该什么都没有，不能当成不限。
	var portraitFields []string
	if _, present := c.GetQuery("portrait_field"); present {
		portraitFields = []string{}
		for _, field := range strings.Split(c.Query("portrait_field"), ",") {
			if field = strings.TrimSpace(field); knownFields[field] {
				portraitFields = append(portraitFields, field)
			}
		}
	}
	var statuses []string
	if _, present := c.GetQuery("status"); present {
		statuses = []string{}
		for _, status := range strings.Split(c.Query("status"), ",") {
			if status = strings.TrimSpace(status); relationshipEvaluationStatuses[status] {
				statuses = append(statuses, status)
			}
		}
	}
	// 多取一条判断还有没有下一页，省一次计数查询。
	records, err := h.sqlite.ListRelationshipEvaluations(c.Request.Context(), assistant.RelationshipEvaluationFilter{
		BotProfileID: botProfileScope(c),
		UserID:       strings.TrimSpace(c.Query("user_id")),
		GroupID:      strings.TrimSpace(c.Query("group_id")),
		Query:        strings.TrimSpace(c.Query("q")),
		Person:       strings.TrimSpace(c.Query("person")),
		Since:        since,
		Direction: allowedValue(c.Query("direction"), assistant.RelationshipDirectionUp, assistant.RelationshipDirectionDown,
			assistant.RelationshipDirectionChanged, assistant.RelationshipDirectionNone),
		ChatKind:       allowedValue(c.Query("chat"), assistant.RelationshipChatGroup, assistant.RelationshipChatPrivate),
		PortraitFields: portraitFields,
		PortraitSource: allowedValue(c.Query("portrait_source"), assistant.PortraitSourceStated, assistant.PortraitSourceInferred),
		MinConfidence:  minConfidence,
		Model:          strings.TrimSpace(c.Query("model")),
		Statuses:       statuses,
		HasPortrait:    c.Query("portrait") == "1",
		BeforeID:       beforeID,
		Limit:          limit + 1,
	})
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "relationship_evaluations_list", err, "", nil)
		return
	}
	response := relationshipEvaluationsResponse{Evaluations: records, PortraitFields: assistant.PortraitFieldSpecs()}
	if len(records) > limit {
		response.Evaluations = records[:limit]
		response.NextBeforeID = records[limit-1].ID
	}
	c.JSON(http.StatusOK, response)
}
