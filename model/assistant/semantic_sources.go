package assistant

import (
	"context"
	"strings"
)

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
