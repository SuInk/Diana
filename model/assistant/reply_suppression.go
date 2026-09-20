// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const (
	// 暂停时长在这个区间里随机取，不再固定三十分钟：固定值等于给对方一个精确的
	// 时刻表，而且每次都一样的「恰好三十分钟」本身就很像机器。
	replySuppressionNoticeTimeout     = 60 * time.Second
	replySuppressionMinDuration       = 10 * time.Minute
	replySuppressionMaxDuration       = 30 * time.Minute
	replySuppressionMarker            = "[[DIANA_IGNORE_CURRENT_USER_30M]]"
	replyRefusalMarker                = "[[DIANA_REFUSE_CURRENT]]"
	replyRefusalThreshold             = 4
	replyRefusalWindow                = 30 * time.Minute
	botReplyLoopThreshold             = 3
	botReplyLoopWindow                = 30 * time.Minute
	botReplyLoopAIConfidenceThreshold = 0.90
	botReplyLoopClassificationTimeout = 20 * time.Second
	replyRefusalAuditConfidence       = 0.90
)

var replySuppressionAccountPattern = regexp.MustCompile(`[1-9][0-9]{4,13}`)

var errReplySuppressedBeforeSend = errors.New("diana: reply suppressed before send")

// errReplyLoopDetected 表示发送前审核认定这一来一回已经在空转，且累计次数够了。
// 这条回复因此不发出去，暂停也从这一刻起生效。
var errReplyLoopDetected = errors.New("chatbot: reply loop detected before send")

// errReplySelfRepeatDropped 表示这条候选回复只是把机器人自己说过的话又说了一遍。
// 只丢这一条，不开降欲望也不暂停：对方下一条要是带来了新东西，照常回答。
var errReplySelfRepeatDropped = errors.New("chatbot: candidate reply repeats the bot's own recent replies")

type replySuppressionSendGuardKey struct{}

type replySuppressionOutboundGateKey struct{}

type replySuppressionOutboundGate struct {
	mu   sync.Mutex
	refs int
}

type replyControlIntent struct {
	RefuseCurrent       bool
	SuppressCurrentUser bool
	DeliveryMode        replyDeliveryMode
	LineBreakMode       replyLineBreakMode
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
	MeaninglessLoop bool `json:"meaningless_loop"`
	// PurposelessLoop 是「回得很密、而且这一连串来回没有明确任务」：漫无目的地互相接戏、
	// 续剧情、斗嘴。下棋报步、解题、一起做事且在推进的不算。
	PurposelessLoop bool `json:"purposeless_loop"`
	// SelfRepeat 只看机器人自己最近几条回复：这一条是不是把它们又说了一遍。
	//
	// 另外三项都要先对发送者或这一来一回下判断——对方是不是 AI、对方这条有没有内容、
	// 这串来回密不密。机器人和另一台机器人互道晚安时这三项全落空：对方的话像真人，
	// 每条都有内容，密度也说不上异常，可机器人自己已经把同一句「晚安、被窝、明天那页」
	// 换着说了七遍。判据换成只看自己说过什么，那七遍才藏不住。
	//
	// 字面统计做不了这件事：2026-09-20 那段循环用字符二元组 Dice、内容字重合、新词率
	// 三种度量回放 5 天 2959 条发言，都和正常对话分不开——重复的是「又道了一次别」这个
	// 语义动作，措辞每条都新，而群里大量连续答同一个技术问题的发言在字面上比它还重复。
	SelfRepeat bool    `json:"self_repeat"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// counts 决定这次结论算不算一次空转。只有「没内容」「没目的」或「在复读自己」才算：
// 对方是不是 AI 只记录不计数——两台 AI 正经下棋、做题，不该因为对面是 AI 就被停掉。
func (decision botReplyLoopAIDecision) counts() bool {
	if !decision.MeaninglessLoop && !decision.PurposelessLoop && !decision.SelfRepeat {
		return false
	}
	return decision.confident()
}

// damps 决定这次结论要不要进降欲望。降欲望是按账号的：开了以后这个人后面没点名的
// 消息一律不接，直到保留期过去。「没内容」和「没目的」说的是这一整串来回的状态，
// 按账号收口说得通；「在复读自己」说的只是候选回复这一条——下一条要是带来了新东西，
// 本来就该照常回答，不该被前一条的结论连坐。所以复读只丢当前这条（见
// selfRepeatDropsReply），不进这一层。
func (decision botReplyLoopAIDecision) damps() bool {
	if !decision.MeaninglessLoop && !decision.PurposelessLoop {
		return false
	}
	return decision.confident()
}

// selfRepeatDropsReply 报告这条候选回复是不是该就地丢掉：判到复读自己就不发这一条，
// 不牵连这个账号后面的消息。
func (decision botReplyLoopAIDecision) selfRepeatDropsReply() bool {
	return decision.SelfRepeat && decision.confident()
}

func (decision botReplyLoopAIDecision) confident() bool {
	return decision.Confidence >= botReplyLoopAIConfidenceThreshold && decision.Confidence <= 1
}

// replyDampingCause 把这次空转结论翻译成写进事件理由的那句话。复读自己不在其中：
// 它只丢当前这条回复，不开降欲望，见 botReplyLoopAIDecision.damps。
func replyDampingCause(decision botReplyLoopAIDecision) string {
	if decision.MeaninglessLoop {
		return replyDampingCauseMeaningless
	}
	return replyDampingCausePurposeless
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
	reply, deliveryMode, lines := consumeReplyFormatting(reply)
	// 拒答标志参与计数但不展示；账号处置标志仅保留旧版兼容解码。
	intent := replyControlIntent{
		RefuseCurrent:       strings.Contains(reply, replyRefusalMarker),
		SuppressCurrentUser: strings.Contains(reply, replySuppressionMarker),
		DeliveryMode:        deliveryMode,
		LineBreakMode:       lines,
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
	return restoreReplyControlIntent(reply, intent)
}

func restoreReplyControlIntent(reply string, intent replyControlIntent) string {
	if intent.RefuseCurrent {
		reply += replyRefusalMarker
	}
	if intent.SuppressCurrentUser {
		reply += replySuppressionMarker
	}
	return replyDeliveryMarker(intent.DeliveryMode) + replyLineBreakMarker(intent.LineBreakMode) + reply
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
	r.resetPrivateClosingUser(userID)
	r.recordReplySuppression(event, item, "response_suppression_activated", "已限制该用户触发机器人回复", persistErr)
	return item, true
}

// randomReplySuppressionDuration 在 [replySuppressionMinDuration, replySuppressionMaxDuration]
// 里取一个随机时长。用 math/rand 就够：这不是安全边界，只是不想每次都停一样久。
func randomReplySuppressionDuration() time.Duration {
	spread := replySuppressionMaxDuration - replySuppressionMinDuration
	if spread <= 0 {
		return replySuppressionMinDuration
	}
	return replySuppressionMinDuration + time.Duration(rand.Int63n(int64(spread)+1))
}

func (r *Runtime) newReplySuppression(event MessageEvent, reason string, now time.Time) (ReplySuppression, bool) {
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || cfg.IsOwnerEvent(event) || userID == strings.TrimSpace(cfg.BotAccount) {
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
		Until:            now.Add(randomReplySuppressionDuration()),
	}, true
}

func (r *Runtime) activeReplySuppression(event MessageEvent, now time.Time) (ReplySuppression, bool) {
	if r == nil {
		return ReplySuppression{}, false
	}
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || cfg.IsOwnerEvent(event) {
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
		r.recordReplySuppression(event, item, "response_suppression_released", "主人已解除用户响应限制", persistErr)
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
	if userID == "" || botID == "" || userID == botID {
		return botReplyLoopCandidate{}, false
	}
	switch event.Kind {
	case EventKindGroup:
		if r.isGroupDisabled(strings.TrimSpace(event.ProfileID), event.GroupID) {
			return botReplyLoopCandidate{}, false
		}
	case EventKindPrivate:
		// 私聊没有「群被停用」这一层，也没有触发词那一套：一条私聊天然就是在跟
		// 机器人说话。以前这里直接按事件类型挡掉，于是那 57 条私聊里的空转判断
		// 一次都没跑过——判据本身（一来一回都没有内容且重复了好几轮）在私聊里
		// 同样成立，挡掉它没有道理。
		//
		// 但要跟着同一道时间门：空转说的是「这一来一回」，机器人刚才没说过话就
		// 谈不上空转，私聊里的第一句也不该为此多付一次模型调用。
		if !r.privateFollowUpAuditDue(event, time.Now()) {
			return botReplyLoopCandidate{}, false
		}
	default:
		return botReplyLoopCandidate{}, false
	}
	// routingDirected 和「引用了机器人那条」在这里是同一件事的两种形态：都表示这条
	// 消息冲着机器人来。结构形态（@、引用、名字）以外还要认语义形态，否则相关度
	// 分支放行的回复永远进不了空转判断——2026-09-20 深夜 1049765710 群里就是这样：
	// 另一台机器人和 Diana 互道晚安刷了十几轮，每条评分都是「在跟机器人说话：是」，
	// 但正文里既没有 @ 也没有名字，bot_reply_loop_classification 从 23:56 起就再没
	// 跑过一次，回复欲望衰减的密度计数自然也一直是空的。
	directBotFollowup := eventRepliesToBot(event, cfg) || event.routingDirected
	if strings.TrimSpace(readableEventText(event, text)) == "" || (!directBotFollowup && !r.shouldHandleChat(event, text)) {
		return botReplyLoopCandidate{}, false
	}
	if r.shouldHandleResolver(event, text) {
		return botReplyLoopCandidate{}, false
	}
	if event.Kind == EventKindPrivate {
		return botReplyLoopCandidate{TriggerKind: "private"}, true
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
	// 结构上找不到触发点，但评分模型认定对方在跟机器人说话。单独一种 trigger_kind：
	// 这一支的判据来自模型而不是消息本身，日志里要能和 mention/quote/alias 分开看。
	if event.routingDirected {
		return botReplyLoopCandidate{TriggerKind: "directed"}, true
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
	reason := fmt.Sprintf("空转检测：%d 分钟内累计 %d 次高置信度空转（无内容或无目的的来回），本次置信度 %.2f", int(botReplyLoopWindow/time.Minute), len(hits), decision.Confidence)
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
		Action:  "bot_reply_loop_classification",
		Message: "模型已完成 AI 自动回复判断",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID, "user_id": event.UserID, "trigger_kind": candidate.TriggerKind,
			"automated_ai_reply": decision.AutomatedAIReply, "meaningless_loop": decision.MeaninglessLoop,
			"purposeless_loop": decision.PurposelessLoop, "self_repeat": decision.SelfRepeat,
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
	r.resetReplyDampingUser(userID)
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
	if userID == "" || cfg.IsOwnerEvent(event) || userID == strings.TrimSpace(cfg.BotAccount) {
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
	r.recordReplyDampingSend(event, now)
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
	// 暂停先生效，再提示。原先顺序反着：通知发失败就直接 return，暂停跟着一起不生效，
	// 攒够了次数却还在继续回。提示发不出去不该影响暂停。
	r.activateReplySuppressionWithinOutboundGate(event, reason, now)
	hintBase := withReplySuppressionOutboundGateHeld(withReplySuppressionSendGuard(context.Background()))
	hintCtx, cancel := context.WithTimeout(hintBase, replySuppressionNoticeTimeout)
	defer cancel()
	r.sendReplyPauseHint(hintCtx, event, item)
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
	return cfg.IsOwnerEvent(event) && replySuppressionOwnerCommandKind(text) != ""
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

// 暂停时提示一句，但这句必须由人设生成，不能是写死的模板。
//
// 以前三处各有一条硬编码：「为避免机器人互相循环，已暂停响应此账号约 30 分钟，期间
// 不再接续消息。」「短时间内已累计拒绝 N 次请求，现暂停响应此账号…」「为避免继续
// 自动循环，我会暂停响应此账号约 30 分钟」。它们读起来像系统弹窗，而且把「账号」
// 「响应」「暂停」这套后台词汇直接倒进群聊。现在统一走 generateReplyPauseHint，由主
// 模型带人设写一句自然的话；写不出来就什么都不发——宁可不说，也不要退回模板。
//
// 提示里不说具体多久：时长本来就是 10 到 30 分钟之间随机的（见
// randomReplySuppressionDuration），报一个精确数字既不准，也正是那股机器味的来源。
func (r *Runtime) sendReplyPauseHint(ctx context.Context, event MessageEvent, item ReplySuppression) {
	hint, err := r.generateReplyPauseHint(ctx, event)
	if err != nil || hint == "" {
		r.recordReplySuppressionNotice(event, item, false, err, nil)
		return
	}
	msg := OutgoingMessage{Text: hint}
	if event.Kind == EventKindGroup {
		msg.GroupID = event.GroupID
	} else {
		msg.UserID = event.UserID
	}
	sendErr := r.sendOutgoing(ctx, event, msg)
	r.recordReplySuppressionNotice(event, item, true, nil, sendErr)
}

func (r *Runtime) generateReplyPauseHint(ctx context.Context, event MessageEvent) (string, error) {
	ctx = withLLMUsagePurpose(ctx, "reply_suppression_notice")
	messages := r.withUserFacingPersona(event, []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: strings.TrimSpace(`用你自己的语气说一句话，告诉对方你接下来一段时间不接话了。
要求：
1. 就是随口提一句，像人要去忙别的了那样，不是系统通知。
2. 不要说具体停多久，不要出现「暂停」「响应」「账号」「循环」「检测」这类词。
3. 不要解释原因，不要责怪对方，不要说教。
4. 只输出一句自然中文纯文本，不得使用 @、账号、昵称、引用、CQ 码、Markdown、表情或引号。
5. 最多 30 个汉字。`),
		},
		{Role: llm.RoleUser, Content: "现在说这一句。"},
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
	hint := sanitizeReplyPauseHint(raw)
	if hint == "" {
		return "", fmt.Errorf("收声提示为空或含有不该出现的内容")
	}
	return hint, nil
}

// sanitizeReplyPauseHint 拦掉点名、账号和后台词汇：这句话要读起来像人随口说的，
// 漏出「暂停响应此账号」里的任何一个词都会立刻把它打回系统通知。
func sanitizeReplyPauseHint(raw string) string {
	if strings.Contains(raw, "@") || strings.Contains(raw, "[CQ:") || replySuppressionAccountPattern.MatchString(raw) {
		return ""
	}
	// 中文引号一起剥掉：提示词里写了不要加引号，模型照样常给「」括起来，留着就成了
	// 一句被引述的话，不是它自己在说。
	raw = strings.Trim(strings.TrimSpace(raw), "`\"' 「」“”‘’")
	raw = PlainText(CQToSegments(raw))
	raw = normalizeChatWhitespace(raw)
	for _, banned := range []string{"暂停", "响应", "账号", "循环", "检测", "系统"} {
		if strings.Contains(raw, banned) {
			return ""
		}
	}
	if len([]rune(raw)) > 30 {
		return ""
	}
	return strings.TrimSpace(raw)
}

func (r *Runtime) recordReplySuppressionNotice(event MessageEvent, item ReplySuppression, llmGenerated bool, generationErr, sendErr error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	action := "response_suppression_notice_sent"
	message := "响应限制提示已发送"
	kind := applog.KindOperation
	level := applog.LevelInfo
	detail := ""
	if sendErr != nil {
		action = "response_suppression_notice_failed"
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
	r.recordReplySuppression(event, item, "response_suppression_blocked", "响应限制已拦截用户消息", nil)
}
