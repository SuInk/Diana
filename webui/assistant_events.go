// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
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
	Handled   bool   `json:"handled"`
	GroupName string `json:"group_name,omitempty"`
}

type assistantEventsResponse struct {
	Range             string                 `json:"range"`
	Result            string                 `json:"result"`
	Since             *time.Time             `json:"since,omitempty"`
	Events            []assistantEventDetail `json:"events"`
	Total             int64                  `json:"total"`
	FilteredTotal     int64                  `json:"filtered_total"`
	Replied           int64                  `json:"replied"`
	NotReplied        int64                  `json:"not_replied"`
	Pending           int64                  `json:"pending"`
	Errors            int64                  `json:"errors"`
	Notices           int64                  `json:"notices"`
	LLMCalls          int64                  `json:"llm_calls"`
	InputTokens       int64                  `json:"input_tokens"`
	OutputTokens      int64                  `json:"output_tokens"`
	TotalTokens       int64                  `json:"total_tokens"`
	CachedInputTokens int64                  `json:"cached_input_tokens"`
	// LLMDurationMS 是这个范围内所有模型调用的墙钟耗时之和；
	// OutputTokensPerSecond 由它和输出 token 算出，不是各次调用速率的平均。
	LLMDurationMS         int64   `json:"llm_duration_ms"`
	OutputTokensPerSecond float64 `json:"output_tokens_per_second"`
	// AvgTTFTMS 只统计流式跑通的调用；TTFTCalls 为 0 表示这段范围没有可信样本。
	AvgTTFTMS float64 `json:"avg_ttft_ms"`
	TTFTCalls int64   `json:"ttft_calls"`
	Page      int     `json:"page"`
	Limit     int     `json:"limit"`
	HasMore   bool    `json:"has_more"`
	// Group 是当前筛选的群号，Groups 是这个时间范围内可选的群。
	Group  string                    `json:"group,omitempty"`
	Groups []assistantEventGroupItem `json:"groups"`
	// ContextBudget 只在筛了具体某个群时给出：预算是按群算的，全部事件混在
	// 一起没有一个「这个群的预算」可言。
	ContextBudget *assistant.ContextBudgetBreakdown `json:"context_budget,omitempty"`
}

// assistantEventGroupItem 在事件数之外补上群名：筛选器只列群号的话，
// 机器人进了几十个群时根本认不出要看的是哪个。
type assistantEventGroupItem struct {
	storage.InboundEventGroup
	GroupName string `json:"group_name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// namedEventGroups 给筛选器里的群补上名字。名字取自控制台已有的那份群列表缓存，
// 拿不到就只留群号——筛选器少个名字可以忍，为它多打一次 OneBot 请求不值得。
func (h *BotHandler) namedEventGroups(ctx context.Context, profileID string, groups []storage.InboundEventGroup) []assistantEventGroupItem {
	items := make([]assistantEventGroupItem, 0, len(groups))
	names := map[string]string{}
	if live, _, _ := h.consoleGroupSources(ctx, profileID, false); len(live) > 0 {
		for _, group := range live {
			if name := strings.TrimSpace(group.GroupName); name != "" {
				names[strings.TrimSpace(group.GroupID)] = name
			}
		}
	}
	for _, group := range groups {
		groupID := strings.TrimSpace(group.GroupID)
		items = append(items, assistantEventGroupItem{
			InboundEventGroup: group,
			GroupName:         names[groupID],
			// 头像地址是纯拼接，不需要额外请求；由后端给而不是前端拼，
			// 免得把 QQ 的地址格式写死在界面里——别的平台不长这样。
			AvatarURL: assistant.OneBotGroupAvatarURL(groupID),
		})
	}
	return items
}

type assistantEventTraceResponse struct {
	EventID   string                `json:"event_id"`
	MessageID string                `json:"message_id,omitempty"`
	Steps     []storage.AppLogEntry `json:"steps"`
}

func (h *BotHandler) eventTrace(c *gin.Context) {
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

func (h *BotHandler) eventImage(c *gin.Context) {
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
	if c.Query("thumbnail") == "1" {
		if thumbnail, thumbnailType, thumbnailErr := eventImageThumbnail(body); thumbnailErr == nil {
			body = thumbnail
			contentType = thumbnailType
		}
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.Header("Content-Disposition", "inline")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, body)
}

const eventImageThumbnailMaxDimension = 192

func eventImageThumbnail(body []byte) ([]byte, string, error) {
	source, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, "", image.ErrFormat
	}
	targetWidth, targetHeight := width, height
	if width > eventImageThumbnailMaxDimension || height > eventImageThumbnailMaxDimension {
		if width >= height {
			targetWidth = eventImageThumbnailMaxDimension
			targetHeight = max(1, height*eventImageThumbnailMaxDimension/width)
		} else {
			targetHeight = eventImageThumbnailMaxDimension
			targetWidth = max(1, width*eventImageThumbnailMaxDimension/height)
		}
	}
	thumbnail := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sourceY := bounds.Min.Y + y*height/targetHeight
		for x := 0; x < targetWidth; x++ {
			sourceX := bounds.Min.X + x*width/targetWidth
			thumbnail.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, thumbnail); err != nil {
		return nil, "", err
	}
	return encoded.Bytes(), "image/png", nil
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

func (h *BotHandler) listEvents(c *gin.Context) {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "result 仅支持 all、replied、not_replied、pending、error、notice"})
		return
	}
	page := queryPositiveInt(c.Query("page"), 1)
	limit := queryPositiveInt(c.Query("limit"), 50)
	if limit > 100 {
		limit = 100
	}
	groupID := strings.TrimSpace(c.Query("group"))
	profileID := strings.TrimSpace(c.Query("profile"))
	stored, err := h.sqlite.ListInboundEventDetails(c.Request.Context(), storage.InboundEventQuery{
		Since:     since,
		Limit:     limit,
		Offset:    (page - 1) * limit,
		Result:    resultFilter,
		GroupID:   groupID,
		ProfileID: profileID,
	})
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
	groupNames := map[string]string{}
	if live, _, _ := h.consoleGroupSources(c.Request.Context(), profileID, false); len(live) > 0 {
		for _, group := range live {
			groupNames[strings.TrimSpace(group.GroupID)] = strings.TrimSpace(group.GroupName)
		}
	}
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
			GroupName:          groupNames[strings.TrimSpace(item.GroupID)],
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
		Range:             rangeID,
		Result:            string(resultFilter),
		Events:            events,
		Total:             stored.Total,
		FilteredTotal:     stored.FilteredTotal,
		Replied:           stored.Replied,
		NotReplied:        stored.NotReplied,
		Pending:           stored.Pending,
		Errors:            stored.Errors,
		Notices:           stored.Notices,
		LLMCalls:          stored.LLMCalls,
		InputTokens:       stored.InputTokens,
		OutputTokens:      stored.OutputTokens,
		TotalTokens:       stored.TotalTokens,
		CachedInputTokens: stored.CachedInputTokens,

		LLMDurationMS:         stored.LLMDurationMS,
		OutputTokensPerSecond: stored.OutputTokensPerSecond,
		AvgTTFTMS:             stored.AvgTTFTMS,
		TTFTCalls:             stored.TTFTCalls,

		Page:    page,
		Limit:   limit,
		HasMore: int64(page*limit) < stored.FilteredTotal,
		Group:   groupID,
		Groups:  []assistantEventGroupItem{},
	}
	if groups, err := h.sqlite.ListInboundEventGroups(c.Request.Context(), since, botProfileScope(c)); err == nil {
		response.Groups = h.namedEventGroups(c.Request.Context(), botProfileScope(c), groups)
	} else {
		// 筛选器列不出来不该让整页打不开：事件本身已经查到了。
		log.Printf("assistant events: list groups failed: %v", err)
	}
	if budgetRuntime, ok := h.runtime.(contextBudgetRuntime); ok && groupID != "" {
		breakdown := budgetRuntime.ContextBudgetBreakdownForGroup(groupID)
		response.ContextBudget = &breakdown
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
