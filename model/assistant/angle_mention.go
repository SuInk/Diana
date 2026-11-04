// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
)

var angleMentionPattern = regexp.MustCompile(mentionCodeSpanAlternation + `<@!?([A-Za-z0-9_-]{1,64})>`)

// Only platform mention tokens are consumed; HTML, comparisons and code stay literal.
func normalizeAngleMentions(text string, acceptable func(string) bool) string {
	return angleMentionPattern.ReplaceAllStringFunc(text, func(token string) string {
		if isMentionCodeSpan(token) {
			return token
		}
		id := angleMentionPattern.FindStringSubmatch(token)[1]
		if acceptable(id) {
			return mentionMarkerFor(id)
		}
		return ""
	})
}

func qqOfficialMentionSegments(text, quotedID string) (string, []MessageSegment) {
	var segments []MessageSegment
	if quotedID = strings.TrimSpace(quotedID); quotedID != "" {
		segments = append(segments, MessageSegment{Type: "reply", Data: map[string]string{"id": quotedID}})
	}
	appendText := func(body string) {
		if body != "" {
			segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": body}})
		}
	}
	last := 0
	for _, bounds := range angleMentionPattern.FindAllStringSubmatchIndex(text, -1) {
		if bounds[2] < 0 { // Protected code, not a platform mention.
			continue
		}
		appendText(text[last:bounds[0]])
		segments = append(segments, MessageSegment{Type: "at", Data: map[string]string{"qq": text[bounds[2]:bounds[3]]}})
		last = bounds[1]
	}
	if last == 0 {
		return text, platformTextSegments(text, quotedID)
	}
	appendText(text[last:])
	// RawMessage is display text; the segments retain the actual targets.
	return strings.TrimSpace(plainTextWithOptions(segments, plainTextOptions{renderReply: func(string) string { return "" }})), segments
}
