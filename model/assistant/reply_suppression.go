// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const (
	replySuppressionDuration          = 30 * time.Minute
	replySuppressionMarker            = "[[DIANA_IGNORE_CURRENT_USER_30M]]"
	replyRefusalMarker                = "[[DIANA_REFUSE_CURRENT]]"
	replyRefusalThreshold             = 3
	replyRefusalWindow                = 30 * time.Minute
	botReplyLoopThreshold             = 3
	botReplyLoopWindow                = 30 * time.Minute
	botReplyLoopAIConfidenceThreshold = 0.90
	botReplyLoopClassificationTimeout = 20 * time.Second
	replySuppressionNoticeTimeout     = 60 * time.Second
	replyRefusalAuditConfidence       = 0.90
)

var replySuppressionAccountPattern = regexp.MustCompile(`[1-9][0-9]{4,13}`)

var errReplySuppressedBeforeSend = errors.New("diana: reply suppressed before send")

// errReplyLoopDetected 表示发送前审核认定这一来一回已经在空转，且累计次数够了。
// 这条回复因此不发出去，暂停也从这一刻起生效。
var errReplyLoopDetected = errors.New("chatbot: reply loop detected before send")

type replySuppressionSendGuardKey struct{}

type replySuppressionOutboundGateKey struct{}

type replySuppressionOutboundGate struct {
	mu   sync.Mutex
	refs int
}

type replyControlIntent struct {
	RefuseCurrent       bool
	SuppressCurrentUser bool
}

// ReplySuppression is a restart-safe temporary refusal to answer one chat user.
type ReplySuppression struct {
	UserID           string    `json:"user_id"`
	GroupID          string    `json:"group_id,omitempty"`
	TriggerMessageID string    `json:"trigger_message_id,omitempty"`
	Reason           string    `json:"reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	Until            time.Time `json:"until"`
}

type botReplyLoopHit struct {
	MessageID       string
	QuotedMessageID string
	TriggerKind     string
	Confidence      float64
	ObservedAt      time.Time
}

type botReplyLoopState struct {
	UserID string
	Hits   []botReplyLoopHit
}

type replyRefusalHit struct {
	MessageKey string
	ObservedAt time.Time
}

type replyRefusalState struct {
	Hits []replyRefusalHit
}

type botReplyLoopCandidate struct {
	TriggerKind     string
	QuotedMessageID string
}

type botReplyLoopAIDecision struct {
	AutomatedAIReply bool `json:"automated_ai_reply"`
	// MeaninglessLoop 覆盖「对方不是机器人，但这一来一回已经没有任何实质内容」
	// 的情况：双方都只在应付彼此，机器人却还在一条条认真回。判据见分类器提示词。
	MeaninglessLoop bool    `json:"meaningless_loop"`
	Confidence      float64 `json:"confidence"`
	Reason          string  `json:"reason"`
}

func (decision botReplyLoopAIDecision) counts() bool {
	if !decision.AutomatedAIReply && !decision.MeaninglessLoop {
		return false
	}
	return decision.Confidence >= botReplyLoopAIConfidenceThreshold && decision.Confidence <= 1
}

type botReplyLoopClassificationPayload struct {
	CurrentText string `json:"current_text"`
	// BotReplyText 是机器人刚刚为这条消息发出去的回复。判断挪到回复之后做，就是
	// 为了能看到它：一来一回摆在一起，才看得出这是不是在为没有内容的话反复接茬。
	BotReplyText             string   `json:"bot_reply_text,omitempty"`
	QuotedBotText            string   `json:"quoted_bot_text,omitempty"`
	TriggerKind              string   `json:"trigger_kind"`
	RecentSameSenderMessages []string `json:"recent_same_sender_messages,omitempty"`
	RecentBotReplies         []string `json:"recent_bot_replies,omitempty"`
}

func consumeReplyControlIntent(reply string) (string, replyControlIntent) {
	// 保留旧版输出的兼容解码，避免滚动升级期间旧提示生成的标记泄漏。
	// 新提示明确禁止模型输出这些标记，正常控制结论来自发送前审核。
	intent := replyControlIntent{
		RefuseCurrent:       strings.Contains(reply, replyRefusalMarker),
		SuppressCurrentUser: strings.Contains(reply, replySuppressionMarker),
	}
	reply = strings.ReplaceAll(reply, replyRefusalMarker, "")
	reply = strings.ReplaceAll(reply, replySuppressionMarker, "")
	reply = strings.TrimSpace(reply)
	return reply, intent
}

// markdownPlain 必须一路传到 normalizeReply：OneBot v11 不渲染 Markdown，标志断在这里
// 会让 cfg.MarkdownToPlain 形同虚设，** 之类的标记直接漏进聊天窗口。
func normalizeReplyPreservingControlIntent(reply string, maxRunes int, markdownPlain ...bool) string {
	reply, intent := consumeReplyControlIntent(reply)
	reply = normalizeReply(reply, maxRunes, markdownPlain...)
	if intent.RefuseCurrent {
		reply += replyRefusalMarker
	}
	if intent.SuppressCurrentUser {
		reply += replySuppressionMarker
	}
	return reply
}

func withReplySuppressionSendGuard(ctx context.Context) context.Context {
	return context.WithValue(ctx, replySuppressionSendGuardKey{}, true)
}

func replySuppressionSendGuardEnabled(ctx context.Context) bool {
	guarded, _ := ctx.Value(replySuppressionSendGuardKey{}).(bool)
	return guarded
}

func withoutReplySuppressionSendGuard(ctx context.Context) context.Context {
	return context.WithValue(ctx, replySuppressionSendGuardKey{}, false)
}

func withReplySuppressionOutboundGateHeld(ctx context.Context) context.Context {
	return context.WithValue(ctx, replySuppressionOutboundGateKey{}, true)
}

func replySuppressionOutboundGateHeld(ctx context.Context) bool {
	held, _ := ctx.Value(replySuppressionOutboundGateKey{}).(bool)
	return held
}

func (r *Runtime) withReplySuppressionOutboundGate(ctx context.Context, event MessageEvent, run func(context.Context) error) error {
	if replySuppressionOutboundGateHeld(ctx) {
		return run(ctx)
	}
	userID := strings.TrimSpace(event.UserID)
	if r == nil || userID == "" {
		return run(withReplySuppressionOutboundGateHeld(ctx))
	}
	r.replyOutboundGateMu.Lock()
	if r.replyOutboundGates == nil {
		r.replyOutboundGates = map[string]*replySuppressionOutboundGate{}
	}
	gate := r.replyOutboundGates[userID]
	if gate == nil {
		gate = &replySuppressionOutboundGate{}
		r.replyOutboundGates[userID] = gate
	}
	gate.refs++
	r.replyOutboundGateMu.Unlock()
	gate.mu.Lock()
	defer func() {
		gate.mu.Unlock()
		r.replyOutboundGateMu.Lock()
		gate.refs--
		if gate.refs == 0 && r.replyOutboundGates[userID] == gate {
			delete(r.replyOutboundGates, userID)
		}
		r.replyOutboundGateMu.Unlock()
	}()
	return run(withReplySuppressionOutboundGateHeld(ctx))
}

func (r *Runtime) loadReplySuppressions(ctx context.Context, store ReplySuppressionStore, now time.Time) error {
	if r == nil {
		return nil
	}
	var loaded []ReplySuppression
	if store != nil {
		items, _, err := store.LoadReplySuppressions(ctx)
		if err != nil {
			return err
		}
		loaded = items
	}
	r.replySuppressMu.Lock()
	defer r.replySuppressMu.Unlock()
	r.replySuppressions = store
	r.replySuppressByUser = make(map[string]ReplySuppression, len(loaded))
	for _, item := range loaded {
		item.UserID = strings.TrimSpace(item.UserID)
		if item.UserID == "" || !item.Until.After(now) {
			continue
		}
		r.replySuppressByUser[item.UserID] = item
	}
	return nil
}

func (r *Runtime) activateReplySuppression(event MessageEvent, reason string, now time.Time) (ReplySuppression, bool) {
	if r == nil {
		return ReplySuppression{}, false
	}
	var item ReplySuppression
	var activated bool
	_ = r.withReplySuppressionOutboundGate(context.Background(), event, func(context.Context) error {
		item, activated = r.activateReplySuppressionWithinOutboundGate(event, reason, now)
		return nil
	})
	return item, activated
}

func (r *Runtime) activateReplySuppressionWithinOutboundGate(event MessageEvent, reason string, now time.Time) (ReplySuppression, bool) {
	item, ok := r.newReplySuppression(event, reason, now)
	if !ok {
		return ReplySuppression{}, false
	}
	userID := item.UserID
	r.replySuppressMu.Lock()
	if r.replySuppressByUser == nil {
		r.replySuppressByUser = map[string]ReplySuppression{}
	}
	if existing, ok := r.replySuppressByUser[userID]; ok && existing.Until.After(item.CreatedAt) {
		r.replySuppressMu.Unlock()
		return existing, false
	}
	r.replySuppressByUser[userID] = item
	persistErr := r.persistReplySuppressionsLocked()
	r.replySuppressMu.Unlock()
	r.resetBotReplyLoopUser(userID)
	r.resetReplyRefusalUser(userID)
	r.recordReplySuppression(event, item, "diana.response_suppression.activated", "已限制该用户触发机器人回复", persistErr)
	return item, true
}

func (r *Runtime) newReplySuppression(event MessageEvent, reason string, now time.Time) (ReplySuppression, bool) {
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || userID == strings.TrimSpace(cfg.OwnerID) || userID == strings.TrimSpace(cfg.BotAccount) {
		return ReplySuppression{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	return ReplySuppression{
		UserID:           userID,
		GroupID:          strings.TrimSpace(event.GroupID),
		TriggerMessageID: strings.TrimSpace(event.MessageID),
		Reason:           truncateRunesFromStart(strings.TrimSpace(reason), 240),
		CreatedAt:        now,
		Until:            now.Add(replySuppressionDuration),
	}, true
}

func (r *Runtime) activeReplySuppression(event MessageEvent, now time.Time) (ReplySuppression, bool) {
	if r == nil {
		return ReplySuppression{}, false
	}
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || userID == strings.TrimSpace(cfg.OwnerID) {
		return ReplySuppression{}, false
	}
	r.replySuppressMu.Lock()
	defer r.replySuppressMu.Unlock()
	item, ok := r.replySuppressByUser[userID]
	if !ok {
		return ReplySuppression{}, false
	}
	if !item.Until.After(now) {
		delete(r.replySuppressByUser, userID)
		return ReplySuppression{}, false
	}
	return item, true
}

func (r *Runtime) clearReplySuppression(event MessageEvent, userID string) (ReplySuppression, bool) {
	userID = strings.TrimSpace(userID)
	if r == nil || userID == "" {
		return ReplySuppression{}, false
	}
	r.replySuppressMu.Lock()
	item, ok := r.replySuppressByUser[userID]
	if ok {
		delete(r.replySuppressByUser, userID)
	}
	persistErr := r.persistReplySuppressionsLocked()
	r.replySuppressMu.Unlock()
	r.resetBotReplyLoopUser(userID)
	r.resetReplyRefusalUser(userID)
	if ok {
		r.recordReplySuppression(event, item, "diana.response_suppression.released", "主人已解除用户响应限制", persistErr)
	}
	return item, ok
}

func (r *Runtime) botReplyLoopCandidate(event MessageEvent, text string) (botReplyLoopCandidate, bool) {
	if r == nil {
		return botReplyLoopCandidate{}, false
	}
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	// 主人同样进入判断：空转和身份无关，主人也会跟机器人互相说废话。真正不能对
	// 主人做的是暂停，那一层由 replyAuditNeed 的 LoopSuppress 单独控制。
	if event.Kind != EventKindGroup || userID == "" || botID == "" || userID == botID || r.isGroupDisabled(strings.TrimSpace(event.ProfileID), event.GroupID) {
		return botReplyLoopCandidate{}, false
	}
	directBotFollowup := eventRepliesToBot(event, cfg)
	if strings.TrimSpace(readableEventText(event, text)) == "" || (!directBotFollowup && !r.shouldHandleChat(event, text)) {
		return botReplyLoopCandidate{}, false
	}
	if r.shouldHandleResolver(event, text) {
		return botReplyLoopCandidate{}, false
	}
	if event.Quoted != nil && strings.TrimSpace(event.Quoted.UserID) == botID {
		return botReplyLoopCandidate{TriggerKind: "quote", QuotedMessageID: strings.TrimSpace(event.Quoted.MessageID)}, true
	}
	for _, mentionedID := range mentionedUserIDs(event.Segments) {
		if strings.TrimSpace(mentionedID) == botID {
			return botReplyLoopCandidate{TriggerKind: "mention"}, true
		}
	}
	if strings.Contains(event.RawMessage, "[CQ:at,qq="+botID+"]") {
		return botReplyLoopCandidate{TriggerKind: "mention"}, true
	}
	if len(matchedGroupAliases(event, cfg, text)) > 0 {
		return botReplyLoopCandidate{TriggerKind: "alias"}, true
	}
	if event.ToMe {
		return botReplyLoopCandidate{TriggerKind: "direct"}, true
	}
	return botReplyLoopCandidate{}, false
}

// botReplyLoopEvidence 是判断空转要用的近期上下文。它跟着发送前审核一起发出去，
// 不再单独占一次模型调用（见 auditReplyBeforeSend）。
type botReplyLoopEvidence struct {
	RecentSameSenderMessages []string
	RecentBotReplies         []string
}

// collectBotReplyLoopEvidence 取同一发送者最近几条消息和机器人最近几条回复。
// 空转是「一来一回都没有内容、而且重复了好几轮」，只看单条看不出来。
func (r *Runtime) collectBotReplyLoopEvidence(event MessageEvent, history []MessageEvent) botReplyLoopEvidence {
	evidence := botReplyLoopEvidence{}
	botID := firstNonEmpty(strings.TrimSpace(r.effectiveConfigForEvent(event).BotAccount), strings.TrimSpace(event.SelfID))
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if len(evidence.RecentSameSenderMessages) < 5 &&
			item.MessageID != event.MessageID &&
			strings.TrimSpace(item.UserID) == strings.TrimSpace(event.UserID) {
			if text := strings.TrimSpace(historyPlainText(item)); text != "" {
				evidence.RecentSameSenderMessages = append(evidence.RecentSameSenderMessages, truncateRunesFromStart(text, 400))
			}
		}
		if len(evidence.RecentBotReplies) < 3 {
			if strings.TrimSpace(item.botReply) != "" || assistantHistoryEvent(item, botID) {
				if text := strings.TrimSpace(firstNonEmpty(item.botReply, historyPlainText(item))); text != "" {
					evidence.RecentBotReplies = append(evidence.RecentBotReplies, truncateRunesFromStart(text, 300))
				}
			}
		}
		if len(evidence.RecentSameSenderMessages) >= 5 && len(evidence.RecentBotReplies) >= 3 {
			break
		}
	}
	reverseStrings(evidence.RecentSameSenderMessages)
	reverseStrings(evidence.RecentBotReplies)
	return evidence
}

func reverseStrings(items []string) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func (r *Runtime) registerBotReplyLoopDecision(event MessageEvent, candidate botReplyLoopCandidate, decision botReplyLoopAIDecision, now time.Time) (int, string, bool) {
	userID := strings.TrimSpace(event.UserID)
	key := botReplyLoopKey(event, userID)
	observedAt := now
	if event.Time > 0 {
		observedAt = time.Unix(event.Time, 0)
	}
	r.botReplyLoopMu.Lock()
	if r.botReplyLoopByKey == nil {
		r.botReplyLoopByKey = map[string]botReplyLoopState{}
	}
	state := r.botReplyLoopByKey[key]
	state.UserID = userID
	hits := state.Hits[:0]
	for _, hit := range state.Hits {
		age := observedAt.Sub(hit.ObservedAt)
		if age >= 0 && age <= botReplyLoopWindow {
			hits = append(hits, hit)
		}
	}
	if !decision.counts() {
		state.Hits = hits
		if len(hits) == 0 {
			delete(r.botReplyLoopByKey, key)
		} else {
			r.botReplyLoopByKey[key] = state
		}
		r.botReplyLoopMu.Unlock()
		return len(hits), "", false
	}
	messageID := strings.TrimSpace(event.MessageID)
	for _, hit := range hits {
		if messageID != "" && hit.MessageID == messageID {
			state.Hits = hits
			r.botReplyLoopByKey[key] = state
			r.botReplyLoopMu.Unlock()
			return len(hits), "", false
		}
	}
	hits = append(hits, botReplyLoopHit{
		MessageID:       messageID,
		QuotedMessageID: candidate.QuotedMessageID,
		TriggerKind:     candidate.TriggerKind,
		Confidence:      decision.Confidence,
		ObservedAt:      observedAt,
	})
	if len(hits) < botReplyLoopThreshold {
		state.Hits = hits
		r.botReplyLoopByKey[key] = state
		r.botReplyLoopMu.Unlock()
		return len(hits), "", false
	}
	delete(r.botReplyLoopByKey, key)
	r.botReplyLoopMu.Unlock()
	reason := fmt.Sprintf("AI 发言检测：%d 分钟内累计 %d 次高置信度自动 AI 回复，本次置信度 %.2f", int(botReplyLoopWindow/time.Minute), len(hits), decision.Confidence)
	return len(hits), reason, true
}

func (r *Runtime) recordBotReplyLoopClassification(ctx context.Context, event MessageEvent, candidate botReplyLoopCandidate, decision botReplyLoopAIDecision, hitCount int, raw string, classifyErr error, suppressionAllowed bool) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "diana.bot_reply_loop_classification",
		Message: "模型已完成 AI 自动回复判断",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID, "user_id": event.UserID, "trigger_kind": candidate.TriggerKind,
			"automated_ai_reply": decision.AutomatedAIReply, "meaningless_loop": decision.MeaninglessLoop,
			"confidence": decision.Confidence,
			"reason":     decision.Reason, "counted": decision.counts(), "hit_count": hitCount,
			"threshold": botReplyLoopThreshold, "window_minutes": int(botReplyLoopWindow / time.Minute),
			"suppression_allowed": suppressionAllowed,
		},
	}
	if hitCount >= botReplyLoopThreshold && !suppressionAllowed {
		entry.Message = "已累计到空转阈值，但当前发言者不适用暂停，仅记录"
	}
	if classifyErr != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "AI 自动回复判断失败，已放行消息"
		entry.Detail = classifyErr.Error()
		entry.Metadata["raw"] = truncateRunesFromStart(strings.TrimSpace(raw), 240)
	}
	_ = writer.AppendLog(ctx, entry)
}

func botReplyLoopKey(event MessageEvent, userID string) string {
	return sessionKey(event) + "\x00" + strings.TrimSpace(userID)
}

func (r *Runtime) resetBotReplyLoopUser(userID string) {
	if r == nil || strings.TrimSpace(userID) == "" {
		return
	}
	r.botReplyLoopMu.Lock()
	for key, state := range r.botReplyLoopByKey {
		if state.UserID == userID {
			delete(r.botReplyLoopByKey, key)
		}
	}
	r.botReplyLoopMu.Unlock()
}

func (r *Runtime) registerReplyRefusal(event MessageEvent, now time.Time) (int, string, bool) {
	if r == nil {
		return 0, "", false
	}
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || userID == strings.TrimSpace(cfg.OwnerID) || userID == strings.TrimSpace(cfg.BotAccount) {
		return 0, "", false
	}
	if now.IsZero() {
		now = time.Now()
	}
	messageKey := sessionKey(event) + "\x00" + strings.TrimSpace(event.MessageID)
	r.replyRefusalMu.Lock()
	if r.replyRefusalByUser == nil {
		r.replyRefusalByUser = map[string]replyRefusalState{}
	}
	state := r.replyRefusalByUser[userID]
	hits := state.Hits[:0]
	for _, hit := range state.Hits {
		age := now.Sub(hit.ObservedAt)
		if age >= 0 && age <= replyRefusalWindow {
			hits = append(hits, hit)
		}
	}
	if strings.TrimSpace(event.MessageID) != "" {
		for _, hit := range hits {
			if hit.MessageKey == messageKey {
				state.Hits = hits
				r.replyRefusalByUser[userID] = state
				r.replyRefusalMu.Unlock()
				return len(hits), "", false
			}
		}
	}
	hits = append(hits, replyRefusalHit{MessageKey: messageKey, ObservedAt: now})
	if len(hits) < replyRefusalThreshold {
		state.Hits = hits
		r.replyRefusalByUser[userID] = state
		r.replyRefusalMu.Unlock()
		return len(hits), "", false
	}
	state.Hits = hits
	r.replyRefusalByUser[userID] = state
	r.replyRefusalMu.Unlock()
	reason := fmt.Sprintf("通用拒答：%d 分钟内累计 %d 次已成功发送的当前消息拒答", int(replyRefusalWindow/time.Minute), len(hits))
	return len(hits), reason, true
}

func (r *Runtime) resetReplyRefusalUser(userID string) {
	if r == nil || strings.TrimSpace(userID) == "" {
		return
	}
	r.replyRefusalMu.Lock()
	delete(r.replyRefusalByUser, strings.TrimSpace(userID))
	r.replyRefusalMu.Unlock()
}

func (r *Runtime) applyReplyControlAfterSend(ctx context.Context, event MessageEvent, reply string, intent replyControlIntent) {
	if !replySuppressionOutboundGateHeld(ctx) {
		_ = r.withReplySuppressionOutboundGate(ctx, event, func(gatedCtx context.Context) error {
			r.applyReplyControlAfterSend(gatedCtx, event, reply, intent)
			return nil
		})
		return
	}
	now := time.Now()
	if intent.SuppressCurrentUser {
		r.activateReplySuppressionWithinOutboundGate(event, reply, now)
		return
	}
	if !intent.RefuseCurrent {
		return
	}
	if _, blocked := r.activeReplySuppression(event, now); blocked {
		return
	}
	_, reason, thresholdReached := r.registerReplyRefusal(event, now)
	if !thresholdReached {
		return
	}
	item, ok := r.newReplySuppression(event, reason, now)
	if !ok {
		return
	}
	noticeBase := withReplySuppressionOutboundGateHeld(withReplySuppressionSendGuard(context.Background()))
	noticeCtx, cancel := context.WithTimeout(noticeBase, replySuppressionNoticeTimeout)
	defer cancel()
	if err := r.sendReplyRefusalCooldownNotice(noticeCtx, event, item); err != nil {
		return
	}
	r.activateReplySuppressionWithinOutboundGate(event, reason, now)
}

func (r *Runtime) persistReplySuppressionsLocked() error {
	if r.replySuppressions == nil {
		return nil
	}
	items := make([]ReplySuppression, 0, len(r.replySuppressByUser))
	for _, item := range r.replySuppressByUser {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Until.Before(items[j].Until) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.replySuppressions.SaveReplySuppressions(ctx, items)
}

func (r *Runtime) listReplySuppressions(now time.Time) []ReplySuppression {
	r.replySuppressMu.Lock()
	defer r.replySuppressMu.Unlock()
	items := make([]ReplySuppression, 0, len(r.replySuppressByUser))
	for userID, item := range r.replySuppressByUser {
		if !item.Until.After(now) {
			delete(r.replySuppressByUser, userID)
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Until.Before(items[j].Until) })
	return items
}

func (r *Runtime) isOwnerReplySuppressionCommand(event MessageEvent, text string) bool {
	cfg := r.effectiveConfigForEvent(event)
	return strings.TrimSpace(cfg.OwnerID) != "" && event.UserID == cfg.OwnerID && replySuppressionOwnerCommandKind(text) != ""
}

func replySuppressionOwnerCommandKind(text string) string {
	command := normalizeChatWhitespace(strings.TrimSpace(text))
	switch {
	case command == "响应限制" || command == "响应限制 列表" || command == "查看响应限制":
		return "list"
	case command == "解除响应限制" || command == "恢复响应限制" || command == "取消响应限制":
		return "release"
	// 以前这里还有一条「命令里同时出现『响应限制』和 解除/恢复/取消 任一词」的模糊
	// 分支。那是拿同义词组合在整句话里找意图，一句正常聊天只要凑齐这两个词就会被当成
	// 主人命令劫持掉。命令识别只保留下面这些固定前缀和上面的整句相等匹配。
	case strings.HasPrefix(command, "解除响应限制 ") || strings.HasPrefix(command, "恢复响应 ") || strings.HasPrefix(command, "取消忽略 ") || strings.HasPrefix(command, "解除忽略 "):
		return "release"
	default:
		return ""
	}
}

func (r *Runtime) handleReplySuppressionOwnerCommand(event MessageEvent, text string) (string, bool) {
	kind := replySuppressionOwnerCommandKind(text)
	if kind == "" {
		return "", false
	}
	if kind == "list" {
		items := r.listReplySuppressions(time.Now())
		if len(items) == 0 {
			return "当前没有生效中的响应限制。", true
		}
		lines := []string{"当前响应限制："}
		for _, item := range items {
			lines = append(lines, fmt.Sprintf("- 账号 %s，剩余 %s", item.UserID, formatReplySuppressionRemaining(time.Until(item.Until))))
		}
		return strings.Join(lines, "\n"), true
	}
	target := replySuppressionCommandTarget(event, text, r.effectiveConfigForEvent(event))
	if target == "" {
		return "请 @ 要解除限制的用户，或使用：响应限制 解除 <账号>。", true
	}
	if _, ok := r.clearReplySuppression(event, target); !ok {
		return fmt.Sprintf("账号 %s 当前没有生效中的响应限制。", target), true
	}
	return fmt.Sprintf("已解除账号 %s 的响应限制，后续消息可以正常触发。", target), true
}

func replySuppressionCommandTarget(event MessageEvent, text string, cfg BotConfig) string {
	botID := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount))
	for _, userID := range mentionedUserIDs(event.Segments) {
		if userID != botID && userID != strings.TrimSpace(cfg.OwnerID) {
			return userID
		}
	}
	if event.Quoted != nil {
		if userID := strings.TrimSpace(event.Quoted.UserID); userID != "" && userID != botID && userID != strings.TrimSpace(cfg.OwnerID) {
			return userID
		}
	}
	for _, userID := range replySuppressionAccountPattern.FindAllString(text, -1) {
		if userID != botID && userID != strings.TrimSpace(cfg.OwnerID) && userID != strings.TrimSpace(event.GroupID) {
			return userID
		}
	}
	return ""
}

func formatReplySuppressionRemaining(remaining time.Duration) string {
	if remaining <= 0 {
		return "已到期"
	}
	minutes := int(remaining.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("约 %d 分钟", minutes)
}

func (r *Runtime) sendReplySuppressionActivationNotice(ctx context.Context, event MessageEvent, item ReplySuppression) {
	notice, generationErr := r.generateReplySuppressionActivationNotice(ctx, event, item)
	llmGenerated := generationErr == nil && notice != ""
	if !llmGenerated {
		notice = "为避免机器人互相循环，已暂停响应此账号" + formatReplySuppressionRemaining(time.Until(item.Until)) + "，期间不再接续消息。"
	}
	msg := OutgoingMessage{Text: notice}
	if event.Kind == EventKindGroup {
		msg.GroupID = event.GroupID
	} else {
		msg.UserID = event.UserID
	}
	sendErr := r.sendOutgoing(ctx, event, msg)
	r.recordReplySuppressionNotice(event, item, llmGenerated, generationErr, sendErr)
}

func (r *Runtime) sendReplyRefusalCooldownNotice(ctx context.Context, event MessageEvent, item ReplySuppression) error {
	msg := OutgoingMessage{Text: fmt.Sprintf(
		"短时间内已累计拒绝 %d 次请求，现暂停响应此账号%s；期间消息不会在到期后补发。",
		replyRefusalThreshold,
		formatReplySuppressionRemaining(time.Until(item.Until)),
	)}
	if event.Kind == EventKindGroup {
		msg.GroupID = event.GroupID
	} else {
		msg.UserID = event.UserID
	}
	sendErr := r.sendOutgoing(ctx, event, msg)
	r.recordReplySuppressionNotice(event, item, false, nil, sendErr)
	return sendErr
}

func (r *Runtime) generateReplySuppressionActivationNotice(ctx context.Context, event MessageEvent, item ReplySuppression) (string, error) {
	ctx = withLLMUsagePurpose(ctx, "reply_suppression_notice")
	messages := r.withUserFacingPersona(event, []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: strings.TrimSpace(`你为 群聊生成一条简短的系统状态提示。
要求：
1. 说明为避免机器人互相循环，机器人已暂时停止响应“此账号”。
2. 说明暂停的大约时长。
3. 只输出一句自然中文纯文本，不要解释检测细节，不要责怪对方。
4. 不得使用 @、账号、昵称、引用、CQ 码、Markdown、表情或引号。
5. 最多 70 个汉字。`),
		},
		{
			Role:    llm.RoleUser,
			Content: "暂停时长：" + formatReplySuppressionRemaining(time.Until(item.Until)),
		},
	})
	callCtx, cancel := context.WithTimeout(ctx, replySuppressionNoticeTimeout)
	defer cancel()
	raw, err := r.runLLMProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return "", err
	}
	notice := sanitizeReplySuppressionNotice(raw)
	if notice == "" || !strings.Contains(notice, "暂停") || !strings.Contains(notice, "响应") || !strings.Contains(notice, "此账号") {
		return "", fmt.Errorf("响应限制提示未包含必要状态")
	}
	return notice, nil
}

func sanitizeReplySuppressionNotice(raw string) string {
	if strings.Contains(raw, "@") || strings.Contains(raw, "[CQ:") || replySuppressionAccountPattern.MatchString(raw) {
		return ""
	}
	raw = strings.Trim(strings.TrimSpace(raw), "`\"' ")
	raw = PlainText(CQToSegments(raw))
	raw = normalizeChatWhitespace(raw)
	if len([]rune(raw)) > 70 {
		raw = string([]rune(raw)[:70])
	}
	return strings.TrimSpace(raw)
}

func (r *Runtime) recordReplySuppression(event MessageEvent, item ReplySuppression, action, message string, operationErr error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	kind := applog.KindOperation
	level := applog.LevelInfo
	detail := ""
	if operationErr != nil {
		kind = applog.KindError
		level = applog.LevelError
		detail = operationErr.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind: kind, Level: level, Action: action, Message: message, Detail: detail,
		Actor: oneBotEventActor(event), Target: item.UserID,
		Metadata: map[string]any{
			"group_id": item.GroupID, "user_id": item.UserID, "until": item.Until,
			"trigger_message_id": item.TriggerMessageID, "reason": item.Reason,
		},
	})
}

func (r *Runtime) recordReplySuppressionBlocked(event MessageEvent, item ReplySuppression) {
	r.recordReplySuppression(event, item, "diana.response_suppression.blocked", "响应限制已拦截用户消息", nil)
}

func (r *Runtime) recordReplySuppressionNotice(event MessageEvent, item ReplySuppression, llmGenerated bool, generationErr, sendErr error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	action := "diana.response_suppression.notice_sent"
	message := "响应限制提示已发送"
	kind := applog.KindOperation
	level := applog.LevelInfo
	detail := ""
	if sendErr != nil {
		action = "diana.response_suppression.notice_failed"
		message = "响应限制提示发送失败"
		kind = applog.KindError
		level = applog.LevelError
		detail = sendErr.Error()
	}
	metadata := map[string]any{
		"group_id": item.GroupID, "user_id": item.UserID, "until": item.Until,
		"trigger_message_id": item.TriggerMessageID, "llm_generated": llmGenerated,
	}
	if generationErr != nil {
		metadata["generation_error"] = generationErr.Error()
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind: kind, Level: level, Action: action, Message: message, Detail: detail,
		Actor: oneBotEventActor(event), Target: item.UserID, Metadata: metadata,
	})
}
