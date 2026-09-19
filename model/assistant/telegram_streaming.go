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
	channel  TextDraftChannel
	message  OutgoingMessage
	draftID  int64
	maxRunes int

	mu       sync.Mutex
	lastSent time.Time
	lastText string
	disabled bool
}

func (r *Runtime) telegramReplyDraft(event MessageEvent, cfg BotConfig) *telegramReplyDraft {
	if NormalizePlatformID(event.Platform) != PlatformTelegram || event.Kind != EventKindPrivate || !boolValue(cfg.LLMStreamingEnabled, true) {
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
	}
}

func (d *telegramReplyDraft) ObserveTextDelta(ctx context.Context, text string) {
	if d == nil || d.channel == nil || ctx.Err() != nil {
		return
	}
	text, _ = consumeReplyControlIntent(text)
	text = strings.TrimSpace(text)
	// Hold an incomplete metadata prefix until it can be stripped safely.
	if text == "" || strings.HasPrefix(replySingleMarker, text) || strings.HasPrefix(replyAutoMarker, text) || strings.HasPrefix(replyLinesPreserveMarker, text) || strings.HasPrefix(replyLinesCompactMarker, text) {
		return
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

// startTypingIndicator 在准备回复期间持续显示「正在输入」：Telegram 群聊私聊都支持，
// OneBot 只支持私聊且可在机器人设置里关闭。
func (r *Runtime) startTypingIndicator(ctx context.Context, event MessageEvent, cfg BotConfig) func() {
	platform := NormalizePlatformID(event.Platform)
	switch platform {
	case PlatformTelegram:
	case PlatformOneBotV11:
		if event.Kind != EventKindPrivate || !boolValue(cfg.QQTypingEnabled, true) {
			return func() {}
		}
	default:
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
		defer recoverGoroutinePanic("telegram_streaming.go:startTypingIndicator")
		// 不是所有 OneBot 实现都有 set_input_status，报错后就不再重复调用。
		stopOnError := platform == PlatformOneBotV11
		if err := channel.SendChatAction(typingCtx, msg, "typing"); err != nil && stopOnError {
			return
		}
		ticker := time.NewTicker(telegramTypingRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				if err := channel.SendChatAction(typingCtx, msg, "typing"); err != nil && stopOnError {
					return
				}
			}
		}
	}()
	return cancel
}
