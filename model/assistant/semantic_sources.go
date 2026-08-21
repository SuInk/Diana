// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type semanticReferenceContext struct {
	Content              string
	RequestedSourceCount int
	ResolvedSourceCount  int
	TextSourceCount      int
	HistoricalImageCount int
	AttachedImageCount   int
	MissingSourceCount   int
}

func semanticSourceMessageIDs(primary string, additional []string) []string {
	values := make([]string, 0, len(additional)+1)
	values = append(values, primary)
	values = append(values, additional...)
	return uniqueNonEmptyStrings(values...)
}

func eventSemanticSourceMessageIDs(event MessageEvent) []string {
	return semanticSourceMessageIDs(event.SemanticSourceMessageID, event.SemanticSourceMessageIDs)
}

func quotedSemanticSourceMessageIDs(quoted *QuotedMessage) []string {
	if quoted == nil {
		return nil
	}
	return semanticSourceMessageIDs(quoted.SemanticSourceMessageID, quoted.SemanticSourceMessageIDs)
}

func setEventSemanticSourceMessageIDs(event *MessageEvent, messageIDs []string) {
	if event == nil {
		return
	}
	messageIDs = uniqueNonEmptyStrings(messageIDs...)
	event.SemanticSourceMessageIDs = append([]string(nil), messageIDs...)
	event.SemanticSourceMessageID = ""
	if len(messageIDs) > 0 {
		event.SemanticSourceMessageID = messageIDs[0]
	}
}

func setQuotedSemanticSourceMessageIDs(quoted *QuotedMessage, messageIDs []string) {
	if quoted == nil {
		return
	}
	messageIDs = uniqueNonEmptyStrings(messageIDs...)
	quoted.SemanticSourceMessageIDs = append([]string(nil), messageIDs...)
	quoted.SemanticSourceMessageID = ""
	if len(messageIDs) > 0 {
		quoted.SemanticSourceMessageID = messageIDs[0]
	}
}

func (r *Runtime) semanticReferenceImageURLs(ctx context.Context, event MessageEvent) []string {
	images, _, _ := r.semanticReferenceImageURLsDetailed(ctx, event)
	return images
}

func (r *Runtime) semanticReferenceImageURLsDetailed(ctx context.Context, event MessageEvent) ([]string, int, error) {
	messageIDs := eventSemanticSourceMessageIDs(event)
	if len(messageIDs) == 0 {
		return nil, 0, nil
	}

	images := make([]string, 0, len(messageIDs))
	skippedImages := 0
	for _, messageID := range messageIDs {
		if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) == messageID {
			quotedEvent := MessageEvent{
				Kind:      event.Kind,
				GroupID:   firstNonEmpty(event.Quoted.GroupID, event.GroupID),
				UserID:    firstNonEmpty(event.Quoted.UserID, event.UserID),
				MessageID: event.Quoted.MessageID,
				Segments:  event.Quoted.Segments,
			}
			originalQuotedEvent := quotedEvent
			quotedEvent = r.prepareHistoricalEventImages(ctx, quotedEvent)
			if historicalImageStateChanged(originalQuotedEvent, quotedEvent) {
				if source, ok := r.findSemanticReferenceEvent(ctx, event, messageID); ok {
					source.Segments = quotedEvent.Segments
					r.updateHistoricalImageState(source)
				}
			}
			skippedImages += unavailableImageSegmentCount(quotedEvent.Segments)
			images = appendUniqueStrings(images, availableImageURLs(quotedEvent.Segments)...)
			continue
		}
		source, ok := r.findSemanticReferenceEvent(ctx, event, messageID)
		if !ok {
			continue
		}
		prepared := r.prepareHistoricalEventImages(ctx, source)
		if historicalImageStateChanged(source, prepared) {
			r.updateHistoricalImageState(prepared)
		}
		source = prepared
		skippedImages += unavailableImageSegmentCount(source.Segments)
		images = appendUniqueStrings(images, availableImageURLs(source.Segments)...)
		if source.Quoted != nil {
			skippedImages += unavailableImageSegmentCount(source.Quoted.Segments)
			images = appendUniqueStrings(images, availableImageURLs(source.Quoted.Segments)...)
		}
	}
	readyImages, complete := loadLLMImageURLs(ctx, images)
	if !complete {
		skippedImages += len(images) - len(readyImages)
	}
	return readyImages, skippedImages, nil
}

func (r *Runtime) semanticReferenceContext(ctx context.Context, event MessageEvent) semanticReferenceContext {
	messageIDs := eventSemanticSourceMessageIDs(event)
	result := semanticReferenceContext{RequestedSourceCount: len(messageIDs)}
	if len(messageIDs) == 0 {
		return result
	}

	botID := firstNonEmpty(strings.TrimSpace(r.effectiveConfigForEvent(event).BotAccount), strings.TrimSpace(event.SelfID))
	lines := []string{"【语义指代选中的历史来源，按下列顺序逐条核对；这些来源不是当前用户的新消息】"}
	for _, messageID := range messageIDs {
		source, found := r.findSemanticReferenceEvent(ctx, event, messageID)
		if !found && event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) == messageID {
			source = MessageEvent{
				Kind:       event.Kind,
				GroupID:    firstNonEmpty(event.Quoted.GroupID, event.GroupID),
				UserID:     event.Quoted.UserID,
				MessageID:  event.Quoted.MessageID,
				RawMessage: event.Quoted.RawMessage,
				Segments:   event.Quoted.Segments,
				SenderName: event.Quoted.SenderName,
			}
			found = true
		}
		if !found {
			result.MissingSourceCount++
			lines = append(lines, fmt.Sprintf("- message_id=%s；status=未找到持久化历史", messageID))
			continue
		}

		result.ResolvedSourceCount++
		text := strings.TrimSpace(historyPlainText(source))
		if text != "" {
			result.TextSourceCount++
		}
		imageCount := historicalStillImageCount(source)
		result.HistoricalImageCount += imageCount
		role := "user"
		if assistantHistoryEvent(source, botID) {
			role = "assistant"
		}
		at := "未知"
		if source.Time > 0 {
			at = time.Unix(source.Time, 0).Local().Format("2006-01-02 15:04:05")
		}
		line := fmt.Sprintf(
			"- message_id=%s；sender=%s；role=%s；time=%s；image_count=%d",
			messageID,
			strings.TrimSpace(source.SenderNameOrID()),
			role,
			at,
			imageCount,
		)
		if text != "" {
			line += "\n  正文：" + text
		} else if imageCount == 0 {
			line += "\n  正文：无可用文本或图片"
		}
		lines = append(lines, line)
	}
	result.Content = strings.Join(lines, "\n")
	return result
}

func semanticReferenceContextMessage(context semanticReferenceContext) llm.Message {
	if strings.TrimSpace(context.Content) == "" {
		return llm.Message{}
	}
	return llm.Message{
		Role:     llm.RoleUser,
		Content:  context.Content,
		Priority: llm.MessagePriorityCurrent,
	}
}
