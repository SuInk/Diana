package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type assistantEventDetail struct {
	storage.InboundEventDetail
	Handled bool `json:"handled"`
}

type assistantEventsResponse struct {
	Range         string                 `json:"range"`
	Result        string                 `json:"result"`
	Since         *time.Time             `json:"since,omitempty"`
	Events        []assistantEventDetail `json:"events"`
	Total         int64                  `json:"total"`
	FilteredTotal int64                  `json:"filtered_total"`
	Replied       int64                  `json:"replied"`
	NotReplied    int64                  `json:"not_replied"`
	Pending       int64                  `json:"pending"`
	Errors        int64                  `json:"errors"`
	LLMCalls      int64                  `json:"llm_calls"`
	InputTokens   int64                  `json:"input_tokens"`
	OutputTokens  int64                  `json:"output_tokens"`
	TotalTokens   int64                  `json:"total_tokens"`
	Page          int                    `json:"page"`
	Limit         int                    `json:"limit"`
	HasMore       bool                   `json:"has_more"`
}

type assistantEventTraceResponse struct {
	EventID   string                `json:"event_id"`
	MessageID string                `json:"message_id,omitempty"`
	Steps     []storage.AppLogEntry `json:"steps"`
}

func (h *QQBotHandler) eventTrace(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	eventID := strings.TrimSpace(c.Param("id"))
	messageID, steps, found, err := h.sqlite.InboundEventDebugTrace(c.Request.Context(), eventID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "事件不存在"})
		return
	}
	c.JSON(http.StatusOK, assistantEventTraceResponse{
		EventID:   eventID,
		MessageID: messageID,
		Steps:     steps,
	})
}

func (h *QQBotHandler) eventImage(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	imageIndex, err := strconv.Atoi(strings.TrimSpace(c.Param("index")))
	if err != nil || imageIndex <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "图片序号无效"})
		return
	}
	segment, found, err := h.sqlite.InboundEventImageSegment(c.Request.Context(), c.Param("id"), imageIndex)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取事件图片失败"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "事件图片不存在"})
		return
	}
	body, contentType, err := assistant.ReadMessageImageSegment(c.Request.Context(), segment)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "图片源已失效或不可读取"})
		return
	}
	contentType, ok := eventRasterImageContentType(contentType)
	if !ok {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "事件图片不是受支持的栅格格式"})
		return
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.Header("Content-Disposition", "inline")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, body)
}

func eventRasterImageContentType(contentType string) (string, bool) {
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "image/bmp", "image/x-icon":
		return contentType, true
	default:
		return "", false
	}
}

func (h *QQBotHandler) listEvents(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	rangeID := strings.TrimSpace(c.DefaultQuery("range", "24h"))
	since, ok := assistantEventsSince(rangeID, time.Now())
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "range 仅支持 1h、24h、7d、30d、all"})
		return
	}
	resultFilter, ok := storage.ParseInboundEventResultFilter(c.DefaultQuery("result", string(storage.InboundEventResultAll)))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "result 仅支持 all、replied、not_replied、pending、error"})
		return
	}
	page := queryPositiveInt(c.Query("page"), 1)
	limit := queryPositiveInt(c.Query("limit"), 50)
	if limit > 100 {
		limit = 100
	}
	stored, err := h.sqlite.ListInboundEventDetails(c.Request.Context(), since, limit, (page-1)*limit, resultFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	recent := map[string]assistant.EventRecord{}
	if h.runtime != nil {
		for _, event := range h.runtime.Status().RecentEvents {
			if key := assistantEventKey(string(event.Kind), event.GroupID, event.UserID, event.MessageID); key != "" {
				recent[key] = event
			}
		}
	}
	events := make([]assistantEventDetail, 0, len(stored.Events))
	for _, item := range stored.Events {
		decision, reason, handled := assistant.DescribeEventOutcome(item.Outcome)
		unconfirmedErrorReply := item.Outcome == "error_replied" && item.DeliveryStage != string(assistant.OutboundDeliveryAcknowledged) && item.DeliveryStage != string(assistant.OutboundDeliveryEchoPersisted)
		if unconfirmedErrorReply {
			decision, handled = "error", false
			reason = "错误说明曾发起发送，但历史记录没有可核验的 ACK 或自回显证据"
		} else if item.Decision != "" {
			decision = item.Decision
			handled = decision == "replied"
		} else if item.Status == "done" && item.Error != "" && decision != "replied" {
			// Older rows may have persisted the processing error before durable
			// decision fields existed. Keep their list badge aligned with the
			// storage-level error filter.
			decision, handled = "error", false
		}
		if item.Reason != "" && !unconfirmedErrorReply {
			reason = item.Reason
		} else if decision == "error" && item.Error != "" && !unconfirmedErrorReply {
			reason = "消息处理失败：" + item.Error
		}
		if item.Status != "done" {
			decision, handled = "pending", false
			if item.Error != "" {
				reason = "处理失败，正在等待自动重试：" + item.Error
			} else if item.Status == "processing" {
				reason = "机器人正在处理这条消息"
			}
		}
		item.Decision = decision
		item.Reason = reason
		detail := assistantEventDetail{
			InboundEventDetail: item,
			Handled:            handled,
		}
		if live, found := recent[assistantEventKey(item.Kind, item.GroupID, item.UserID, item.MessageID)]; found {
			if detail.Platform == "" {
				detail.Platform = live.Platform
			}
			if detail.ProfileID == "" {
				detail.ProfileID = live.ProfileID
			}
			detail.Reply = live.Reply
			if live.Decision != "" && !unconfirmedErrorReply {
				detail.Decision = live.Decision
				detail.Reason = live.Reason
				detail.Handled = live.Handled
			}
			if live.Duration > 0 {
				detail.DurationMS = live.Duration
			}
			if live.Error != "" {
				detail.Error = live.Error
			}
		}
		events = append(events, detail)
	}
	response := assistantEventsResponse{
		Range:         rangeID,
		Result:        string(resultFilter),
		Events:        events,
		Total:         stored.Total,
		FilteredTotal: stored.FilteredTotal,
		Replied:       stored.Replied,
		NotReplied:    stored.NotReplied,
		Pending:       stored.Pending,
		Errors:        stored.Errors,
		LLMCalls:      stored.LLMCalls,
		InputTokens:   stored.InputTokens,
		OutputTokens:  stored.OutputTokens,
		TotalTokens:   stored.TotalTokens,
		Page:          page,
		Limit:         limit,
		HasMore:       int64(page*limit) < stored.FilteredTotal,
	}
	if !since.IsZero() {
		response.Since = &since
	}
	c.JSON(http.StatusOK, response)
}

func assistantEventsSince(rangeID string, now time.Time) (time.Time, bool) {
	switch rangeID {
	case "1h":
		return now.Add(-time.Hour), true
	case "24h":
		return now.Add(-24 * time.Hour), true
	case "7d":
		return now.Add(-7 * 24 * time.Hour), true
	case "30d":
		return now.Add(-30 * 24 * time.Hour), true
	case "all":
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

func queryPositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func assistantEventKey(kind, groupID, userID, messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return ""
	}
	return strings.Join([]string{strings.TrimSpace(kind), strings.TrimSpace(groupID), strings.TrimSpace(userID), messageID}, "|")
}
