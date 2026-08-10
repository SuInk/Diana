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
	Reply    string `json:"reply,omitempty"`
	Handled  bool   `json:"handled"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type assistantEventsResponse struct {
	Range        string                 `json:"range"`
	Since        *time.Time             `json:"since,omitempty"`
	Events       []assistantEventDetail `json:"events"`
	Total        int64                  `json:"total"`
	Replied      int64                  `json:"replied"`
	NotReplied   int64                  `json:"not_replied"`
	Pending      int64                  `json:"pending"`
	Errors       int64                  `json:"errors"`
	LLMCalls     int64                  `json:"llm_calls"`
	InputTokens  int64                  `json:"input_tokens"`
	OutputTokens int64                  `json:"output_tokens"`
	TotalTokens  int64                  `json:"total_tokens"`
	Page         int                    `json:"page"`
	Limit        int                    `json:"limit"`
	HasMore      bool                   `json:"has_more"`
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
	page := queryPositiveInt(c.Query("page"), 1)
	limit := queryPositiveInt(c.Query("limit"), 50)
	if limit > 100 {
		limit = 100
	}
	stored, err := h.sqlite.ListInboundEventDetails(c.Request.Context(), since, limit, (page-1)*limit)
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
		if item.Status != "done" {
			decision, handled = "pending", false
			if item.Error != "" {
				reason = "处理失败，正在等待自动重试：" + item.Error
			} else if item.Status == "processing" {
				reason = "机器人正在处理这条消息"
			}
		}
		detail := assistantEventDetail{
			InboundEventDetail: item,
			Handled:            handled,
			Decision:           decision,
			Reason:             reason,
		}
		if live, found := recent[assistantEventKey(item.Kind, item.GroupID, item.UserID, item.MessageID)]; found {
			if detail.Platform == "" {
				detail.Platform = live.Platform
			}
			if detail.ProfileID == "" {
				detail.ProfileID = live.ProfileID
			}
			detail.Reply = live.Reply
			if live.Decision != "" {
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
		Range:        rangeID,
		Events:       events,
		Total:        stored.Total,
		Replied:      stored.Replied,
		NotReplied:   stored.NotReplied,
		Pending:      stored.Pending,
		Errors:       stored.Errors,
		LLMCalls:     stored.LLMCalls,
		InputTokens:  stored.InputTokens,
		OutputTokens: stored.OutputTokens,
		TotalTokens:  stored.TotalTokens,
		Page:         page,
		Limit:        limit,
		HasMore:      int64(page*limit) < stored.Total,
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
