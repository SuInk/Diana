// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

const (
	// memorySourceWindowSeconds 是来源消息前后各取多大的时间窗。窗只是用来圈候选，
	// 真正返回的条数由下面两个上限决定；热闹的群五分钟也有上百条。
	memorySourceWindowSeconds = 300
	memorySourceBefore        = 4
	memorySourceAfter         = 2
)

type memorySourceMessage struct {
	MessageID  string    `json:"message_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	SenderName string    `json:"sender_name,omitempty"`
	Text       string    `json:"text"`
	Time       time.Time `json:"time,omitempty"`
	Outbound   bool      `json:"outbound,omitempty"`
	Source     bool      `json:"source,omitempty"`
}

type memorySourceResponse struct {
	MemoryID        string `json:"memory_id"`
	Evidence        string `json:"evidence,omitempty"`
	SourceType      string `json:"source_type,omitempty"`
	SourceSession   string `json:"source_session,omitempty"`
	SourceGroupID   string `json:"source_group_id,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	// Origin 说明来源能还原到什么程度：message 找到了原消息；summary 是会话摘要
	// 提炼的，没有单条原消息，只能给出那段时间的聊天；missing 原消息已经不在库里。
	Origin   string                `json:"origin"`
	Note     string                `json:"note,omitempty"`
	Messages []memorySourceMessage `json:"messages"`
}

// getAssistantUserMemorySource 给一条长期记忆找来源：门控当时引用的证据片段、
// 原消息，以及原消息前后几条聊天，让人能核对「这条记忆是从哪句话来的、有没有记歪」。
//
// 和清空接口一样要求显式指定机器人：同一个号在不同机器人下是不同的人员记录。
func (h *BotHandler) getAssistantUserMemorySource(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "人员画像存储未配置"})
		return
	}
	scope, supplied := c.GetQuery("profile")
	if !supplied {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须指定记忆所属机器人"})
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	memoryID := strings.TrimSpace(c.Param("memory"))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	memory, found, err := h.sqlite.GetStructuredMemoryBySubject(ctx, strings.TrimSpace(scope), userID, memoryID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "users_memory_source", err, userID, nil)
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "这条记忆不存在，或不属于这个人"})
		return
	}
	response := memorySourceResponse{
		MemoryID:        memory.ID,
		Evidence:        strings.TrimSpace(memory.Evidence),
		SourceType:      string(memory.SourceType),
		SourceSession:   strings.TrimSpace(memory.SourceSession),
		SourceGroupID:   strings.TrimSpace(memory.SourceGroupID),
		SourceMessageID: strings.TrimSpace(memory.SourceMessageID),
		Messages:        []memorySourceMessage{},
	}
	session := response.SourceSession
	messageID := response.SourceMessageID
	center := memory.SourceEventTime.Unix()
	summarized := strings.HasPrefix(messageID, "summary")
	var source assistant.MessageEvent
	if session != "" && messageID != "" && !summarized {
		source, found, err = h.sqlite.FindMessageEvent(ctx, session, messageID)
		if err != nil {
			h.writeError(c, http.StatusInternalServerError, "users_memory_source", err, userID, nil)
			return
		}
		if found && source.Time > 0 {
			center = source.Time
		}
	} else {
		found = false
	}
	switch {
	case found:
		response.Origin = "message"
	case summarized:
		response.Origin = "summary"
		response.Note = "这条记忆是从一段对话的摘要里提炼的，没有对应的单条原消息；下面是记忆来源时间前后的聊天。"
	default:
		response.Origin = "missing"
		response.Note = "原消息已不在消息记录里（可能已过保留期或被清理）；下面是记忆来源时间前后的聊天。"
	}
	if session == "" || memory.SourceEventTime.IsZero() && !found {
		if response.Origin != "message" {
			response.Note = "没有记下来源会话或时间，无法还原上下文；只能看门控当时引用的证据片段。"
		}
		c.JSON(http.StatusOK, response)
		return
	}
	window, err := h.sqlite.ListMessageEventsBetween(ctx, session, center-memorySourceWindowSeconds, center+memorySourceWindowSeconds)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "users_memory_source", err, userID, nil)
		return
	}
	response.Messages = memorySourceMessages(window, source, found, center)
	c.JSON(http.StatusOK, response)
}

// memorySourceMessages 从时间窗里挑出原消息和它前后几条。找不到原消息时以来源
// 时间为中心，取最接近的那一段。
func memorySourceMessages(window []assistant.MessageEvent, source assistant.MessageEvent, found bool, center int64) []memorySourceMessage {
	if len(window) == 0 {
		if found {
			return []memorySourceMessage{memorySourceMessageFrom(source, true)}
		}
		return []memorySourceMessage{}
	}
	pivot := -1
	if found {
		for index, event := range window {
			if strings.TrimSpace(event.MessageID) == strings.TrimSpace(source.MessageID) {
				pivot = index
				break
			}
		}
	}
	if pivot < 0 {
		// 第一条不早于来源时间的消息；全都更早就取最后一条。
		pivot = len(window) - 1
		for index, event := range window {
			if event.Time >= center {
				pivot = index
				break
			}
		}
	}
	start := max(0, pivot-memorySourceBefore)
	end := min(len(window), pivot+memorySourceAfter+1)
	out := make([]memorySourceMessage, 0, end-start+1)
	if found && strings.TrimSpace(window[pivot].MessageID) != strings.TrimSpace(source.MessageID) {
		// 原消息时间落在窗外（编辑过、时钟漂移），单独补在最前面，不能丢。
		out = append(out, memorySourceMessageFrom(source, true))
	}
	for index := start; index < end; index++ {
		event := window[index]
		out = append(out, memorySourceMessageFrom(event, found && strings.TrimSpace(event.MessageID) == strings.TrimSpace(source.MessageID)))
	}
	return out
}

func memorySourceMessageFrom(event assistant.MessageEvent, isSource bool) memorySourceMessage {
	item := memorySourceMessage{
		MessageID:  strings.TrimSpace(event.MessageID),
		UserID:     strings.TrimSpace(event.UserID),
		SenderName: strings.TrimSpace(event.SenderName),
		Text:       assistant.DisplayEventText(event, nil),
		Outbound:   event.Outbound,
		Source:     isSource,
	}
	if event.Time > 0 {
		item.Time = time.Unix(event.Time, 0).UTC()
	}
	return item
}
