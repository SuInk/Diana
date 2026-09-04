// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	telegramDraftUpdateInterval = 750 * time.Millisecond
	telegramDraftMaxRunes       = 4000
	telegramTypingRenewInterval = 4 * time.Second
)

var telegramDraftSequence atomic.Int64

type telegramReplyDraft struct {
	channel TextDraftChannel
	message OutgoingMessage
	draftID int64

	mu       sync.Mutex
	lastSent time.Time
	lastText string
	disabled bool
}

func (r *Runtime) telegramReplyDraft(event MessageEvent, cfg BotConfig) *telegramReplyDraft {
	if NormalizePlatformID(event.Platform) != PlatformTelegram || event.Kind != EventKindPrivate || !boolValue(cfg.LLMStreamingEnabled, false) {
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
	return &telegramReplyDraft{
		channel: channel,
		message: routeOutgoingToEvent(event, OutgoingMessage{UserID: event.UserID}),
		draftID: draftID,
	}
}

func (d *telegramReplyDraft) ObserveTextDelta(ctx context.Context, text string) {
	if d == nil || d.channel == nil || ctx.Err() != nil {
		return
	}
	text, _ = consumeReplyControlIntent(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	runes := []rune(text)
	if len(runes) > telegramDraftMaxRunes {
		text = string(runes[:telegramDraftMaxRunes])
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disabled || text == d.lastText || (!d.lastSent.IsZero() && time.Since(d.lastSent) < telegramDraftUpdateInterval) {
		return
	}
	msg := d.message
	msg.Text = text
	if err := d.channel.SendTextDraft(ctx, msg, d.draftID); err != nil {
		// Drafts are best effort. A Bot API server that has not implemented
		// sendMessageDraft must not make the final reply fail.
		d.disabled = true
		return
	}
	d.lastText = text
	d.lastSent = time.Now()
}

func (r *Runtime) startTelegramTyping(ctx context.Context, event MessageEvent) func() {
	if NormalizePlatformID(event.Platform) != PlatformTelegram {
		return func() {}
	}
	r.mu.RLock()
	channel, ok := r.channel.(ChatActionChannel)
	r.mu.RUnlock()
	if !ok {
		return func() {}
	}
	typingCtx, cancel := context.WithCancel(ctx)
	msg := routeOutgoingToEvent(event, OutgoingMessage{GroupID: event.GroupID, UserID: event.UserID, MessageThreadID: event.MessageThreadID})
	go func() {
		_ = channel.SendChatAction(typingCtx, msg, "typing")
		ticker := time.NewTicker(telegramTypingRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				_ = channel.SendChatAction(typingCtx, msg, "typing")
			}
		}
	}()
	return cancel
}
