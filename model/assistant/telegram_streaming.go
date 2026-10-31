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
	// qqStream 表示这是 QQ 官方私聊的流式消息：帧一旦发出就是正式消息的一部分。
	qqStream bool

	mu       sync.Mutex
	lastSent time.Time
	lastText string
	disabled bool
}

func (r *Runtime) telegramReplyDraft(event MessageEvent, cfg BotConfig) *telegramReplyDraft {
	if event.webChat || event.Kind != EventKindPrivate || !boolValue(cfg.LLMStreamingEnabled, true) {
		return nil
	}
	platform := NormalizePlatformID(event.Platform)
	qqStream := platform == PlatformQQOfficial
	if platform != PlatformTelegram && !qqStream {
		return nil
	}
	// QQ 的流式消息收尾后不会消失，等于先发后审；这一轮可能要过发送前审核就不流。
	if qqStream && (strings.TrimSpace(event.MessageID) == "" || r.replyAuditMayRun(event, cfg)) {
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
		qqStream: qqStream,
	}
}

// replyAuditMayRun 估计这一轮私聊回复会不会走发送前审核（见 prepareReplyAudit）。
// 疲劳打分要等回复出来才知道，这里只要最近回过这个人就按会审处理。
func (r *Runtime) replyAuditMayRun(event MessageEvent, cfg BotConfig) bool {
	need := r.replyAuditNeed(event, event.RawMessage, cfg, false)
	if need.Quality || need.AccountSafety || need.Loop || need.Closing {
		return true
	}
	if _, blocked := r.activeReplySuppression(event, time.Now()); blocked {
		return true
	}
	return r.replyDensityApplies(event) && r.replyDensityRecent(event, time.Now()) > 0
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
	// 预览看到的是模型原文，还没经过标记扶正；半截标签先不发。
	if pendingDianaMarkupTail(text) {
		return false
	}
	text = normalizeDianaMarkup(text)
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
		if d.qqStream {
			// 帧发过就已经是对方看得到的消息；首帧就失败则照常生成、照常发送。
			return d.lastText != ""
		}
		// A definite Bot API rejection did not publish a preview. Transport
		// failures may happen after delivery, so do not restart generation.
		var apiErr *telegramAPIError
		return !errors.As(err, &apiErr) || apiErr.Code < 400 || apiErr.Code >= 500
	}
	d.lastText = text
	d.lastSent = time.Now()
	return true
}
