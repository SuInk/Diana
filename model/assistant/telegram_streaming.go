// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	telegramDraftUpdateInterval = 750 * time.Millisecond
	telegramDraftMaxRunes       = 4000
)

var telegramDraftSequence atomic.Int64

type telegramReplyDraft struct {
	channel  TextDraftChannel
	message  OutgoingMessage
	draftID  int64
	maxRunes int
	prepare  func(OutgoingMessage) OutgoingMessage

	mu       sync.Mutex
	lastSent time.Time
	lastText string
	disabled bool
}

func (r *Runtime) telegramReplyDraft(event MessageEvent, cfg BotConfig) *telegramReplyDraft {
	if event.webChat || NormalizePlatformID(event.Platform) != PlatformTelegram || event.Kind != EventKindPrivate || !boolValue(cfg.LLMStreamingEnabled, true) {
		return nil
	}
	r.mu.RLock()
	channel, ok := r.channel.(TextDraftChannel)
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	draftID := time.Now().UnixNano() + telegramDraftSequence.Add(1)
	if draftID <= 0 {
		draftID = telegramDraftSequence.Add(1)
	}
	maxRunes := telegramDraftMaxRunes
	if cfg.MaxReplyChars > 0 && cfg.MaxReplyChars < maxRunes {
		maxRunes = cfg.MaxReplyChars
	}
	return &telegramReplyDraft{
		channel:  channel,
		message:  routeOutgoingToEvent(event, OutgoingMessage{UserID: event.UserID}),
		draftID:  draftID,
		maxRunes: maxRunes,
		prepare: func(msg OutgoingMessage) OutgoingMessage {
			return r.resolveOutgoingMentionNames(event, r.normalizeOutgoingMentions(event, msg))
		},
	}
}

// Drafts may expand beyond the source rune budget when usernames or tables are
// rendered. Keep complete runes and clickable entities; formatting can be clipped.
func truncateTelegramDraft(text string, entities []telegramEntitySpec) (string, []telegramEntitySpec) {
	if utf16Length(text) <= telegramTextLimit {
		return text, entities
	}
	limit := telegramTextLimit
	for {
		previous := limit
		for _, entity := range entities {
			switch entity.Type {
			case "mention", "text_mention", "text_link":
				if entity.Offset < limit && entity.Offset+entity.Length > limit {
					limit = entity.Offset
				}
			}
		}
		if limit == previous {
			break
		}
	}
	units, end := 0, len(text)
	for index, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			end = index
			break
		}
		units += width
	}
	kept := make([]telegramEntitySpec, 0, len(entities))
	for _, entity := range entities {
		if entity.Offset < 0 || entity.Offset >= units || entity.Length <= 0 {
			continue
		}
		if entity.Length > units-entity.Offset {
			entity.Length = units - entity.Offset
		}
		kept = append(kept, entity)
	}
	return text[:end], kept
}

func (d *telegramReplyDraft) ObserveTextDelta(ctx context.Context, text string) bool {
	if d == nil || d.channel == nil || ctx.Err() != nil {
		return false
	}
	text, _ = consumeReplyControlIntent(text)
	text = strings.TrimSpace(text)
	// Hold an incomplete metadata prefix until it can be stripped safely.
	if text == "" || strings.HasPrefix(replySingleMarker, text) || strings.HasPrefix(replyAutoMarker, text) || strings.HasPrefix(replyLinesPreserveMarker, text) || strings.HasPrefix(replyLinesCompactMarker, text) {
		return false
	}
	// Draft observers see accumulated provider text before the full response is restored.
	// Resolve against that provider's mapping before truncation or mention cleanup.
	if scope := identityPrivacyScopeFromContext(ctx); scope != nil {
		text = scope.restoreText(text)
	}
	if strings.Contains(text, identityAliasPrefix) {
		return false
	}
	if start := strings.LastIndex(text, "["); start >= 0 {
		pending := text[start:]
		if strings.HasPrefix(dianaMentionMarkerPrefix, pending) || (strings.HasPrefix(pending, dianaMentionMarkerPrefix) && !strings.Contains(pending, "]")) {
			return false
		}
	}
	runes := []rune(text)
	limit := d.maxRunes
	if limit <= 0 {
		limit = telegramDraftMaxRunes
	}
	if len(runes) > limit {
		text = string(runes[:limit])
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disabled || text == d.lastText || (!d.lastSent.IsZero() && time.Since(d.lastSent) < telegramDraftUpdateInterval) {
		return false
	}
	msg := d.message
	msg.Text = text
	if d.prepare != nil {
		msg = d.prepare(msg)
	}
	if err := d.channel.SendTextDraft(ctx, msg, d.draftID); err != nil {
		// Drafts are best effort. A Bot API server that has not implemented
		// sendMessageDraft must not make the final reply fail.
		d.disabled = true
		// A definite Bot API rejection did not publish a preview. Transport
		// failures may happen after delivery, so do not restart generation.
		var apiErr *telegramAPIError
		return !errors.As(err, &apiErr) || apiErr.Code < 400 || apiErr.Code >= 500
	}
	d.lastText = text
	d.lastSent = time.Now()
	return true
}
