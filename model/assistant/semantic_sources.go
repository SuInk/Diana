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
	images, _ := r.semanticReferenceImageURLsDetailed(ctx, event)
	return images
}

func (r *Runtime) semanticReferenceImageURLsDetailed(ctx context.Context, event MessageEvent) ([]string, error) {
	messageIDs := eventSemanticSourceMessageIDs(event)
	if len(messageIDs) == 0 {
		return nil, nil
	}

	images := make([]string, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) == messageID {
			quotedEvent := MessageEvent{
				Kind:      event.Kind,
				GroupID:   firstNonEmpty(event.Quoted.GroupID, event.GroupID),
				UserID:    firstNonEmpty(event.Quoted.UserID, event.UserID),
				MessageID: event.Quoted.MessageID,
				Segments:  event.Quoted.Segments,
			}
			quotedEvent = r.prepareEventImages(ctx, quotedEvent)
			if quotedEvent.imageLoadErr != nil {
				return nil, quotedEvent.imageLoadErr
			}
			images = appendUniqueStrings(images, ImageURLs(quotedEvent.Segments)...)
			continue
		}
		source, ok := r.findSemanticReferenceEvent(ctx, event, messageID)
		if !ok {
			continue
		}
		source = r.prepareEventImages(ctx, source)
		if source.imageLoadErr != nil {
			return nil, source.imageLoadErr
		}
		images = appendUniqueStrings(images, ImageURLs(source.Segments)...)
		if source.Quoted != nil {
			images = appendUniqueStrings(images, ImageURLs(source.Quoted.Segments)...)
		}
	}
	return images, nil
}
