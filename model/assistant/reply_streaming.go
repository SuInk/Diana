// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	replyDraftUpdateInterval = 750 * time.Millisecond
	replyDraftMaxRunes       = 4000
	// qqStreamFirstFrameMinRunes：QQ 流式消息的帧发出去就是对方看得到的正式消息，
	// 首帧攒够几个字再发，请求刚出一个字就被打断时不会只留下一个字。
	qqStreamFirstFrameMinRunes = 8
	// qqStreamAbandonTimeout 是轮次结束后收尾半截流（结束帧加撤回）的时限。
	qqStreamAbandonTimeout = 10 * time.Second
)

var replyDraftSequence atomic.Int64

// replyDraft 将模型文本增量投递为 Telegram 草稿或 QQ 官方私聊流式消息。
type replyDraft struct {
	channel  TextDraftChannel
	message  OutgoingMessage
	draftID  int64
	maxRunes int
	prepare  func(OutgoingMessage) OutgoingMessage
	// qqStream 表示这是 QQ 官方私聊的流式消息：帧一旦发出就是正式消息的一部分。
	qqStream bool
	// chunks 按定稿的规则把截至当前的正文分条，QQ 流式据此一条气泡一条气泡地流。
	chunks func(text string, intent replyControlIntent) []string

	mu sync.Mutex
	// segment 是正在流的第几条；前面的已经定住，不再动。
	segment  int
	lastSent time.Time
	lastText string
	disabled bool
}

func (r *Runtime) replyDraft(event MessageEvent, cfg BotConfig) *replyDraft {
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
	draftID := time.Now().UnixNano() + replyDraftSequence.Add(1)
	if draftID <= 0 {
		draftID = replyDraftSequence.Add(1)
	}
	maxRunes := replyDraftMaxRunes
	if cfg.MaxReplyChars > 0 && cfg.MaxReplyChars < maxRunes {
		maxRunes = cfg.MaxReplyChars
	}
	return &replyDraft{
		channel:  channel,
		message:  routeOutgoingToEvent(event, OutgoingMessage{UserID: event.UserID}),
		draftID:  draftID,
		maxRunes: maxRunes,
		prepare: func(msg OutgoingMessage) OutgoingMessage {
			return r.resolveOutgoingMentionNames(event, r.normalizeOutgoingMentions(event, msg))
		},
		qqStream: qqStream,
		chunks: func(text string, intent replyControlIntent) []string {
			return replyPreviewChunks(cfg, event, text, intent)
		},
	}
}

// replyPreviewChunks 照定稿的路子（prepareGeneratedReply 加 sendDecorated，不含压缩）
// 把还在生成的正文分条。和定稿分得一样，流式里定住的每一条才能被定稿原样认领。
func replyPreviewChunks(cfg BotConfig, event MessageEvent, text string, intent replyControlIntent) []string {
	text = applyReplyFilters(cfg.ReplyFilterRules, text)
	if intent.DeliveryMode != "" {
		event.replyDeliveryMode = intent.DeliveryMode
	}
	if intent.LineBreakMode != "" {
		event.replyLineBreakMode = intent.LineBreakMode
	}
	if chatSplitLimitsForEvent(cfg, event).SingleMessage {
		event.replyDeliveryMode = replyDeliverySingle
	}
	text = formatInlineReplyLists(normalizeExplicitReplyLayout(normalizeReply(text, 0)))
	if event.replyDeliveryMode == replyDeliverySingle {
		text = strings.ReplaceAll(text, notificationSplitMarker, notificationLineMarker)
	}
	var chunks []string
	for _, chunk := range splitEventChatReply(normalizeReply(text, 0, markdownToPlainForConfig(cfg)), cfg, event) {
		if chunk = previewLayout(chunk); chunk != "" {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

// abandonReplyStream 在一轮回复结束后收掉还没定稿的 QQ 私聊流式消息。正常出了
// 正文时首条文字已经发结束帧替换预览、流也已移除，这里什么都不做；静默、出错或
// 被连发打断后没再出正文时，流停在半截，交给通道发结束帧并撤回。
func (r *Runtime) abandonReplyStream(ctx context.Context, event MessageEvent) {
	if event.webChat || event.Kind != EventKindPrivate || NormalizePlatformID(event.Platform) != PlatformQQOfficial || strings.TrimSpace(event.MessageID) == "" {
		return
	}
	r.mu.RLock()
	channel, ok := r.channel.(TextDraftAbandoner)
	r.mu.RUnlock()
	if !ok {
		return
	}
	// 轮次可能是被取消才结束的，收尾不能跟着一起取消。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qqStreamAbandonTimeout)
	defer cancel()
	msg := routeOutgoingToEvent(event, OutgoingMessage{UserID: event.UserID})
	if err := channel.AbandonTextDraft(ctx, msg); err != nil {
		log.Printf("diana qq stream abandon failed: message_id=%s err=%v", event.MessageID, err)
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

// previewLayout 把分条和换行标记换成预览里看得见的排版。定稿由发送层消费这两种
// 标记，预览不经过那一步，原样推出去对方就会看到 <diana-message/>。分条在预览里
// 显示成空行（Telegram 草稿整份显示；QQ 流式先分条，见 replyPreviewChunks）。
func previewLayout(text string) string {
	text = strings.ReplaceAll(text, notificationSplitMarker, "\n\n")
	return strings.TrimSpace(strings.ReplaceAll(text, notificationLineMarker, "\n"))
}

func (d *replyDraft) ObserveTextDelta(ctx context.Context, text string) bool {
	if d == nil || d.channel == nil || ctx.Err() != nil {
		return false
	}
	// 预览看到的是模型原文，还没经过标记扶正；半截标签先不发。
	if pendingDianaMarkupTail(text) {
		return false
	}
	text = normalizeDianaMarkup(text)
	text, intent := consumeReplyControlIntent(text)
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
	if d.qqStream && d.chunks != nil {
		return d.observeQQChunks(ctx, d.chunks(text, intent))
	}
	text = d.clip(previewLayout(text))
	if text == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disabled || text == d.lastText || (!d.lastSent.IsZero() && time.Since(d.lastSent) < replyDraftUpdateInterval) {
		return false
	}
	return d.sendLocked(ctx, text)
}

// observeQQChunks 一条气泡一条气泡地流：后面又出了一条、而且已经攒够首帧的字数，
// 当前这条才算写完——补一帧全文后换下一个流，通道据此把它定住。发出去的字只会
// 往后长；定稿时每条各自认领，见 QQOfficialChannel.settleQQStream。
func (d *replyDraft) observeQQChunks(ctx context.Context, chunks []string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	published := false
	for !d.disabled && d.segment+1 < len(chunks) &&
		(d.segment+2 < len(chunks) || len([]rune(chunks[d.segment+1])) >= qqStreamFirstFrameMinRunes) {
		if text := d.clip(chunks[d.segment]); text != d.lastText {
			if !d.sendLocked(ctx, text) {
				return published
			}
			published = true
		}
		d.segment++
		d.lastText, d.lastSent = "", time.Time{}
	}
	if d.disabled || d.segment >= len(chunks) {
		return published
	}
	text := d.clip(chunks[d.segment])
	if d.lastText == "" && len([]rune(text)) < qqStreamFirstFrameMinRunes {
		return published
	}
	if text == d.lastText || (!d.lastSent.IsZero() && time.Since(d.lastSent) < replyDraftUpdateInterval) {
		return published
	}
	return d.sendLocked(ctx, text) || published
}

func (d *replyDraft) clip(text string) string {
	limit := d.maxRunes
	if limit <= 0 {
		limit = replyDraftMaxRunes
	}
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}

// sendLocked 推一帧预览；调用方持有 d.mu。返回值表示对方是否已经看得到这一轮的预览。
func (d *replyDraft) sendLocked(ctx context.Context, text string) bool {
	text = d.clip(text)
	msg := d.message
	msg.Text = text
	if d.prepare != nil {
		msg = d.prepare(msg)
	}
	if err := d.channel.SendTextDraft(ctx, msg, d.draftID+int64(d.segment)); err != nil {
		// Drafts are best effort. A Bot API server that has not implemented
		// sendMessageDraft must not make the final reply fail.
		d.disabled = true
		if d.qqStream {
			// 帧发过就已经是对方看得到的消息；首帧就失败则照常生成、照常发送。
			return d.lastText != "" || d.segment > 0
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
