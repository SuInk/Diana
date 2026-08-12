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
	messageIDs := eventSemanticSourceMessageIDs(event)
	if len(messageIDs) == 0 {
		return nil
	}

	images := make([]string, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) == messageID {
			images = appendUniqueStrings(images, ImageURLs(event.Quoted.Segments)...)
			continue
		}
		source, ok := r.findSemanticReferenceEvent(ctx, event, messageID)
		if !ok {
			continue
		}
		images = appendUniqueStrings(images, ImageURLs(source.Segments)...)
		if source.Quoted != nil {
			images = appendUniqueStrings(images, ImageURLs(source.Quoted.Segments)...)
		}
	}
	return images
}
