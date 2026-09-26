// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// 同一个人几秒内连发两三条（先发图再说「这个呢」，或者一句话拆成三行），机器人
// 以前每条各回一遍。2026-09-26 一个群一天 26 次，主人直接说了「快闭嘴」。
//
// 追发合并（mergeIntoActiveDirectReply）本来就是管这个的，但它只和「已经开始生成」
// 的那一轮比。第一条往往还卡在路由（主动回复评分、机器人接话判定，动辄几秒），
// 没登记进 activeDirectReplies，第二条看不见它，于是两条各回各的。
//
// 这里在消息一被接手就登记（入站时，早于路由和任何模型调用），按会话+发送者分组，
// 带上到达时间。同一个人后到的那条真正要回复时：
//   - 前一条还没开始生成：由后一条取代，一并回答。取代的范围和提示词点名的范围是
//     同一批——pendingEarlierMessages 认得出的连发，而且显式交给提示词，不会出现
//     「被取代了、提示词却没提」的消息。
//   - 前一条已经在生成：照旧走追发合并，重复、补充、纠正并进去；判成另起一题的，
//     两条都回——不能拿后一条去掐掉一份已经在写的、不同话题的答案。
//   - 前一条已经开始往外发：不动它。半截话比多回一条更糟。
//
// 取代先是暂定的。被取代的那一轮走到回复入口（或发送闸门）时先等后一条的结果：
// 后一条真的回出去了才算数，它自己收住；后一条没回出去（模型不接话、审核拦下、
// 出错……）就把它放回去，自己照常回答。宁可偶尔多等几秒，也不能两条都没人回。
//
// 几条硬边界：
//   - 只在同一个发送者之间，不同的人永远各回各的；
//   - 只管对话回复：链接解析、插件指令的消息既不取代别人，也不被取代——那张卡片
//     丢了没人会补；
//   - 明确叫机器人（@、引用、叫名字）的前一条，不会被后一条随口的主动接话取代。
//
// 登记表只存内存：重启后丢了就退回各回各的，顶多多回一条；误拦一条该发的回复才是事故。
const (
	// senderBurstWindow 是「连发」的判定窗口，按两条消息的时间算。
	senderBurstWindow = 90 * time.Second
	// senderTurnRetention 是登记项的兜底有效期。正常情况下一轮结束就注销，
	// 这里只清理那些没走到收尾的（过期丢弃、断线回补等）。
	senderTurnRetention = 5 * time.Minute
	// senderBurstSettleTimeout 是被暂定取代的那一轮最多等多久。等不到结果就当作
	// 没被取代，自己回答。
	senderBurstSettleTimeout = 3 * time.Minute
	// senderImageFollowUpWindow 和 senderImageFollowUpMaxRunes 是「先发图、再补一句
	// 短话」按规则直接算补充的门槛：几十秒内、十几个字以内，几乎都是在说那张图。
	senderImageFollowUpWindow   = 60 * time.Second
	senderImageFollowUpMaxRunes = 30
)

type senderTurnStage int

const (
	// senderTurnRouting：消息已接手，还在预处理和回复判断里。
	senderTurnRouting senderTurnStage = iota
	// senderTurnReplying：已经进了回复入口，正在生成。
	senderTurnReplying
	// senderTurnSending：第一条回复已经过了发送闸门，之后不再被取代。
	senderTurnSending
)

type senderTurn struct {
	seq       uint64
	event     MessageEvent
	arrivedAt time.Time
	stage     senderTurnStage
	// chatReply 表示它进回复入口时是一轮对话回复（不是链接解析、插件指令）。
	chatReply bool
	// supersededBy 是取代它的那条消息 ID；final 为假时取代还只是暂定的，
	// settled 在取代落定或撤销时关闭。
	supersededBy string
	final        bool
	settled      chan struct{}
	// viaDependency 表示它是作为候选依赖图被文字那一轮接走的。
	viaDependency bool
	// absorbed 是这一轮暂定取代掉的那些，它结束时逐个落定或撤销。
	absorbed []*senderTurn
	// mergeCheckedRoot 记下预处理阶段已经拿哪一轮做过话题判断，回复入口不再重复问一遍。
	mergeCheckedRoot string
	// notMergedWith 是话题判断认定「不是同一件事」的那几轮，依赖图那条路不再接走它们。
	notMergedWith map[string]bool
}

// noteSenderTurnArrival 登记一条刚接手的消息。重复登记保留最早的到达时间。
func (r *Runtime) noteSenderTurnArrival(event MessageEvent) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	now := time.Now()
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	r.pruneSenderTurnsLocked(now)
	r.ensureSenderTurnLocked(key, event, now)
}

func (r *Runtime) ensureSenderTurnLocked(key string, event MessageEvent, now time.Time) *senderTurn {
	if turn := r.senderTurnLocked(key, strings.TrimSpace(event.MessageID)); turn != nil {
		return turn
	}
	if r.senderTurns == nil {
		r.senderTurns = map[string][]*senderTurn{}
	}
	r.senderTurnSeq++
	turn := &senderTurn{seq: r.senderTurnSeq, event: event, arrivedAt: now}
	r.senderTurns[key] = append(r.senderTurns[key], turn)
	return turn
}

func (r *Runtime) senderTurnLocked(key, messageID string) *senderTurn {
	for _, turn := range r.senderTurns[key] {
		if strings.TrimSpace(turn.event.MessageID) == messageID {
			return turn
		}
	}
	return nil
}

func (r *Runtime) pruneSenderTurnsLocked(now time.Time) {
	for key, turns := range r.senderTurns {
		kept := turns[:0]
		for _, turn := range turns {
			if now.Sub(turn.arrivedAt) <= senderTurnRetention {
				kept = append(kept, turn)
				continue
			}
			// 被清掉的那一轮要是还压着别人的暂定取代，先放回去，别让对方一直等。
			r.settleAbsorbedLocked(turn, false)
		}
		if len(kept) == 0 {
			delete(r.senderTurns, key)
			continue
		}
		r.senderTurns[key] = kept
	}
}

// finishSenderTurn 在这一轮收尾时注销登记。还没落定的暂定取代一律撤销。
func (r *Runtime) finishSenderTurn(event MessageEvent) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turns := r.senderTurns[key]
	kept := turns[:0]
	for _, turn := range turns {
		if strings.TrimSpace(turn.event.MessageID) == messageID {
			r.settleAbsorbedLocked(turn, false)
			continue
		}
		kept = append(kept, turn)
	}
	if len(kept) == 0 {
		delete(r.senderTurns, key)
		return
	}
	r.senderTurns[key] = kept
}

// settleAbsorbedLocked 落定（sent）或撤销这一轮的暂定取代，返回需要持久化的那些。
func (r *Runtime) settleAbsorbedLocked(turn *senderTurn, sent bool) []MessageEvent {
	var persist []MessageEvent
	laterID := strings.TrimSpace(turn.event.MessageID)
	for _, absorbed := range turn.absorbed {
		if absorbed.supersededBy != laterID || absorbed.final {
			continue
		}
		if sent {
			absorbed.final = true
			// 只有「图那一轮已经在生成对话回复」时才落库：持久化的发送前检查不分
			// 发送种类，落在链接解析、插件指令的消息上会把它们的产出一起拦掉。
			if absorbed.viaDependency && absorbed.chatReply && absorbed.stage == senderTurnReplying {
				persist = append(persist, absorbed.event)
			}
		} else {
			absorbed.supersededBy, absorbed.viaDependency = "", false
		}
		if absorbed.settled != nil {
			close(absorbed.settled)
			absorbed.settled = nil
		}
	}
	turn.absorbed = nil
	return persist
}

// settleSenderBurst 在后一条这一轮结束时调用：真的回出去了，暂定的取代落定；
// 没回出去，就把前面那几条放回去自己回答。
func (r *Runtime) settleSenderBurst(ctx context.Context, event MessageEvent, sent bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	var persist []MessageEvent
	if turn := r.senderTurnLocked(key, messageID); turn != nil {
		persist = r.settleAbsorbedLocked(turn, sent)
	}
	r.replyInterruptMu.Unlock()
	if len(persist) == 0 {
		return
	}
	turnID := messageID
	if turn := outboundTurnFromContext(ctx); turn != nil && strings.TrimSpace(turn.id) != "" {
		turnID = strings.TrimSpace(turn.id)
	}
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundReplyMergeStore)
	r.mu.RUnlock()
	if store == nil {
		return
	}
	for _, source := range persist {
		mergeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if err := store.RecordInboundEventReplyMerge(mergeCtx, source, turnID); err != nil {
			log.Printf("diana record dependency image supersession failed: %v", err)
		}
		cancel()
	}
}

// waitSenderTurnSettled 在这条消息被暂定取代时等后一条的结果，返回落定后的取代者。
// 等不到（超时、ctx 取消）就自己撤销取代，照常回答。
func (r *Runtime) waitSenderTurnSettled(ctx context.Context, event MessageEvent) (string, bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return "", false
	}
	timer := time.NewTimer(senderBurstSettleTimeout)
	defer timer.Stop()
	for {
		r.replyInterruptMu.Lock()
		turn := r.senderTurnLocked(key, messageID)
		if turn == nil || turn.supersededBy == "" {
			r.replyInterruptMu.Unlock()
			return "", false
		}
		if turn.final {
			by := turn.supersededBy
			r.replyInterruptMu.Unlock()
			return by, true
		}
		settled, by := turn.settled, turn.supersededBy
		r.replyInterruptMu.Unlock()
		select {
		case <-settled:
			continue
		case <-ctx.Done():
		case <-timer.C:
		}
		r.replyInterruptMu.Lock()
		if turn.supersededBy == by && !turn.final {
			turn.supersededBy, turn.viaDependency = "", false
			if turn.settled != nil {
				close(turn.settled)
				turn.settled = nil
			}
		}
		final, finalBy := turn.final, turn.supersededBy
		r.replyInterruptMu.Unlock()
		return finalBy, final && finalBy != ""
	}
}

// senderTurnArrivedAt 返回消息的登记到达时间；没登记过时用 fallback。
func (r *Runtime) senderTurnArrivedAt(event MessageEvent, fallback time.Time) time.Time {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return fallback
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if turn := r.senderTurnLocked(key, messageID); turn != nil {
		return turn.arrivedAt
	}
	return fallback
}

// noteSenderTurnMergeChecked 记下这条消息已经和哪一轮做过话题判断、判出来合不合并。
func (r *Runtime) noteSenderTurnMergeChecked(event MessageEvent, rootMessageID string, merged bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	rootMessageID = strings.TrimSpace(rootMessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.ensureSenderTurnLocked(key, event, time.Now())
	turn.mergeCheckedRoot = rootMessageID
	if !merged && rootMessageID != "" {
		if turn.notMergedWith == nil {
			turn.notMergedWith = map[string]bool{}
		}
		turn.notMergedWith[rootMessageID] = true
	}
}

// enterSenderTurnReply 在回复入口把登记推进到「生成中」。没登记过的（直接调用回复
// 入口的路径）在这里补登记。
func (r *Runtime) enterSenderTurnReply(event MessageEvent, chatReply bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.ensureSenderTurnLocked(key, event, time.Now())
	if turn.stage < senderTurnReplying {
		turn.stage = senderTurnReplying
	}
	turn.chatReply = chatReply
	// 预处理可能把回复对象换成了另一条（积压合并、主动回复路由），以回复时的事件为准。
	turn.event = event
}

// markSenderTurnSending 在回复过了发送闸门时调用：从这一刻起它不会再被取代。
func (r *Runtime) markSenderTurnSending(event MessageEvent) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if turn := r.senderTurnLocked(key, messageID); turn != nil && turn.supersededBy == "" {
		turn.stage = senderTurnSending
	}
}

// passSenderTurnSendGate 是发送闸门上的那一步。对话回复被暂定取代时先等结果：
// 取代落定就拦下，撤销了就照发。放行时在同一把锁里记成「开始发送」，之后不再被
// 取代：半截话比多回一条更糟。
func (r *Runtime) passSenderTurnSendGate(ctx context.Context, event MessageEvent, directRun bool) bool {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return true
	}
	for {
		if directRun {
			if _, superseded := r.waitSenderTurnSettled(ctx, event); superseded {
				return false
			}
		}
		r.replyInterruptMu.Lock()
		turn := r.senderTurnLocked(key, messageID)
		if turn == nil {
			r.replyInterruptMu.Unlock()
			return true
		}
		if turn.supersededBy != "" {
			if !directRun {
				// 链接解析、插件指令的产出不受取代影响。
				r.replyInterruptMu.Unlock()
				return true
			}
			// 刚等完又被别的一轮暂定取代了，再等一次。
			r.replyInterruptMu.Unlock()
			continue
		}
		turn.stage = senderTurnSending
		r.replyInterruptMu.Unlock()
		return true
	}
}

// senderTurnSupersededBy 报告这条消息是否已被同一个人后到的消息取代（含暂定）。
func (r *Runtime) senderTurnSupersededBy(event MessageEvent) (string, bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return "", false
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if turn := r.senderTurnLocked(key, messageID); turn != nil && turn.supersededBy != "" {
		return turn.supersededBy, true
	}
	return "", false
}

// directReplyOutcome 圈出走追发合并（beginDirectReply）的轮次。
func directReplyOutcome(event MessageEvent, successOutcome string) bool {
	return successOutcome == "replied" || successOutcome == "replied_direct_followup" || event.proactiveReply || event.chatInReply
}

// burstChatMessage 判断这条消息是不是一轮对话回复：shouldHandle 对链接解析和插件
// 指令同样返回 "replied"，得把它们单独剔出去。
func (r *Runtime) burstChatMessage(event MessageEvent, text string) bool {
	text = firstNonEmpty(strings.TrimSpace(text), directedInboundText(event))
	return !r.shouldHandleResolver(event, text) && !r.shouldHandlePlugin(event, text)
}

// burstMessageDirected 判断这条消息是不是明确在叫机器人（@、引用、叫名字、私聊）。
func (r *Runtime) burstMessageDirected(event MessageEvent) bool {
	return r.shouldHandleChatTrigger(event, directedInboundText(event)) || explicitlyRepliesToBot(event, r.effectiveConfigForEvent(event))
}

// claimSenderBurst 在回复入口处理同一个人的连发。返回 (outcome, true) 表示这一轮
// 到此为止，不再生成。
func (r *Runtime) claimSenderBurst(ctx context.Context, event MessageEvent, text string, successOutcome string) (MessageEvent, string, bool) {
	chat := directReplyOutcome(event, successOutcome) && r.burstChatMessage(event, text)
	r.enterSenderTurnReply(event, chat)
	if !chat {
		return event, "", false
	}
	if supersededBy, superseded := r.waitSenderTurnSettled(ctx, event); superseded {
		record := r.decisionEventRecord(event, text, "superseded_follow_up")
		record.Reason = fmt.Sprintf("同一用户随后又发来消息（%s），由那一轮一并回答", supersededBy)
		r.record(record)
		return event, "superseded_follow_up", true
	}
	// 预处理时前一条可能还没开始生成，那时比不了；现在它开始生成了，补判一次。
	// 预处理阶段已经和这一轮判过的不再重复问。
	if root, ok := r.activeDirectReplyRoot(event); ok && root != strings.TrimSpace(event.MessageID) && !r.senderTurnMergeCheckedAgainst(event, root) {
		if rootMessageID, merged := r.mergeIntoActiveDirectReply(ctx, event, text); merged {
			event.routingReason = fmt.Sprintf("已并入同一用户正在生成的回复（触发消息 %s），不再单独发送", rootMessageID)
			r.record(r.decisionEventRecord(event, text, "merged_into_reply"))
			return event, "merged_into_reply", true
		}
	}
	if absorbed := r.supersedeEarlierSenderTurns(event); len(absorbed) > 0 {
		// 被取代的那几条显式交给提示词点名承接；它们也得真的出现在这一轮的上下文里。
		// 历史是预处理时取的，前一条要是之后才进历史，就让回复重新取一次。
		event.burstAbsorbed = absorbed
		if !eventHistoryContainsAll(event.replyHistory, absorbed) {
			event.replyHistory, event.replyHistoryLoaded = nil, false
		}
		ids := make([]string, 0, len(absorbed))
		for _, item := range absorbed {
			ids = append(ids, strings.TrimSpace(item.MessageID))
		}
		log.Printf("diana sender burst: message %s provisionally supersedes earlier %s from the same sender", strings.TrimSpace(event.MessageID), strings.Join(ids, ","))
	}
	return event, "", false
}

func (r *Runtime) activeDirectReplyRoot(event MessageEvent) (string, bool) {
	key := directReplyMergeKey(event)
	if key == "" {
		return "", false
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	active := r.activeDirectReplies[key]
	if active == nil || !active.accepting {
		return "", false
	}
	return strings.TrimSpace(active.root.MessageID), true
}

func (r *Runtime) senderTurnMergeCheckedAgainst(event MessageEvent, rootMessageID string) bool {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.senderTurnLocked(key, messageID)
	return turn != nil && turn.mergeCheckedRoot != "" && turn.mergeCheckedRoot == strings.TrimSpace(rootMessageID)
}

// senderBurstGap 算两条消息的间隔：两边都有平台时间戳就按时间戳（回放、断线回补
// 的登记顺序不可信），否则按登记的到达时间。
func senderBurstGap(earlier *senderTurn, earlierEvent MessageEvent, later *senderTurn, laterEvent MessageEvent) time.Duration {
	if earlierEvent.Time > 0 && laterEvent.Time > 0 {
		return time.Duration(laterEvent.Time-earlierEvent.Time) * time.Second
	}
	return later.arrivedAt.Sub(earlier.arrivedAt)
}

// supersedeEarlierSenderTurns 让同一个人更早到、还没开始生成的消息暂定让位给当前这条。
//
// 候选只取 pendingEarlierMessages 认得出的那几条连发（紧挨着当前这条、中间没有别人
// 或机器人说话），提示词点名承接的也是这一批，两边永远一致。前一条还没进会话历史
// （比如卡在识图预处理里）就不在候选里，两条各回——多回一条总比有一条谁都没回强。
func (r *Runtime) supersedeEarlierSenderTurns(event MessageEvent) []MessageEvent {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return nil
	}
	r.mu.RLock()
	history := append([]MessageEvent(nil), r.history[sessionKey(event)]...)
	r.mu.RUnlock()
	pending := pendingEarlierMessages(history, event, pendingEarlierMessagesLimit)
	if len(pending) == 0 {
		return nil
	}
	// 随口的主动接话不能取代一条明确叫机器人的消息；链接解析、插件指令不参与。
	laterDirected := !event.proactiveReply && !event.chatInReply
	eligible := make(map[string]bool, len(pending))
	for _, item := range pending {
		if !r.burstChatMessage(item, directedInboundText(item)) {
			continue
		}
		if !laterDirected && r.burstMessageDirected(item) {
			continue
		}
		eligible[strings.TrimSpace(item.MessageID)] = true
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	current := r.senderTurnLocked(key, messageID)
	if current == nil {
		return nil
	}
	var absorbed []MessageEvent
	for _, item := range pending {
		earlierID := strings.TrimSpace(item.MessageID)
		if !eligible[earlierID] {
			continue
		}
		turn := r.senderTurnLocked(key, earlierID)
		if turn == nil || turn == current || turn.stage != senderTurnRouting || turn.supersededBy != "" {
			continue
		}
		gap := senderBurstGap(turn, item, current, event)
		if gap < 0 || gap > senderBurstWindow {
			continue
		}
		turn.supersededBy, turn.final, turn.settled = messageID, false, make(chan struct{})
		current.absorbed = append(current.absorbed, turn)
		absorbed = append(absorbed, item)
	}
	return absorbed
}

func eventHistoryContainsAll(history []MessageEvent, events []MessageEvent) bool {
	ids := make(map[string]bool, len(history))
	for _, item := range history {
		ids[strings.TrimSpace(item.MessageID)] = true
	}
	for _, event := range events {
		if !ids[strings.TrimSpace(event.MessageID)] {
			return false
		}
	}
	return true
}

// supersedeDependencyImageTurns 在回复把同一个人刚发的纯图消息当候选依赖图附上时，
// 让那条图自己的那一轮暂定让位。#816 去掉图文合并后，「先发图、再问这个呢」是两轮：
// 文字那一轮已经带着图在答了，图那一轮再单独回一遍就是重复。
//
// 只处理纯图、还没开始发送的；话题判断已经认定「不是同一件事」的不接走；随口的
// 主动接话不接走明确叫机器人的图。和连发取代一样是暂定的，文字那一轮回出去才落定。
func (r *Runtime) supersedeDependencyImageTurns(event MessageEvent, images []senderDependencyImage) {
	key := directReplyMergeKey(event)
	currentID := strings.TrimSpace(event.MessageID)
	if key == "" || currentID == "" || len(images) == 0 {
		return
	}
	laterDirected := !event.proactiveReply && !event.chatInReply
	seen := map[string]bool{}
	var sources []MessageEvent
	for _, image := range images {
		source := image.Source
		sourceID := strings.TrimSpace(source.MessageID)
		if sourceID == "" || sourceID == currentID || seen[sourceID] || directReplyMergeKey(source) != key {
			continue
		}
		seen[sourceID] = true
		if strings.TrimSpace(historyPlainText(source)) != "" || !r.burstChatMessage(source, "") {
			continue
		}
		if !laterDirected && r.burstMessageDirected(source) {
			continue
		}
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	current := r.senderTurnLocked(key, currentID)
	if current == nil {
		return
	}
	for _, source := range sources {
		sourceID := strings.TrimSpace(source.MessageID)
		if current.notMergedWith[sourceID] {
			continue
		}
		turn := r.senderTurnLocked(key, sourceID)
		if turn == nil || turn.stage >= senderTurnSending || turn.supersededBy != "" {
			continue
		}
		if turn.stage == senderTurnReplying && !turn.chatReply {
			continue
		}
		turn.supersededBy, turn.final, turn.settled, turn.viaDependency = currentID, false, make(chan struct{}), true
		current.absorbed = append(current.absorbed, turn)
		log.Printf("diana sender burst: image message %s provisionally answered together with %s", sourceID, currentID)
	}
}

// senderImageThenShortText 判断「前一条是纯图、后一条是同一个人几十秒内补的一句短话」。
func senderImageThenShortText(root MessageEvent, rootArrived time.Time, event MessageEvent, eventArrived time.Time, text string) bool {
	if strings.TrimSpace(root.UserID) == "" || strings.TrimSpace(root.UserID) != strings.TrimSpace(event.UserID) {
		return false
	}
	if strings.TrimSpace(historyPlainText(root)) != "" || !pendingEarlierImageOnly(root) {
		return false
	}
	// 只认纯文字（可以带 @机器人）：图、链接卡片、文件、转发都不是「补一句话」。
	for _, segment := range event.Segments {
		switch segment.Type {
		case "text", "at", "reply":
		default:
			return false
		}
	}
	text = strings.TrimSpace(firstNonEmpty(textSegmentsOnly(event.Segments), text))
	if text == "" || len([]rune(text)) > senderImageFollowUpMaxRunes || textLooksLikeURL(text) {
		return false
	}
	gap := eventArrived.Sub(rootArrived)
	if root.Time > 0 && event.Time > 0 {
		gap = time.Duration(event.Time-root.Time) * time.Second
	}
	return gap >= 0 && gap <= senderImageFollowUpWindow
}

// imageFollowUpBySameSender 是话题判断前的那条规则：纯图之后同一个人补的一句短话
// 直接算补充。这一步发生在「要不要回」的判断之前，所以要把不是冲着那张图来的
// 挡在外面：带链接的、会触发插件或链接解析的、@ 了别人的、引用了别的消息的。
func (r *Runtime) imageFollowUpBySameSender(root MessageEvent, rootArrived time.Time, event MessageEvent, eventArrived time.Time, text string) bool {
	if !senderImageThenShortText(root, rootArrived, event, eventArrived, text) {
		return false
	}
	if !r.burstChatMessage(event, text) {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	botID := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount))
	for _, id := range mentionedUserIDs(event.Segments) {
		if strings.TrimSpace(id) != botID {
			return false
		}
	}
	if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) != strings.TrimSpace(root.MessageID) {
		return false
	}
	return true
}

// textLooksLikeURL 粗略认链接：带协议头或 www. 的一律不走规则，交给判断器。
func textLooksLikeURL(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "://") || strings.Contains(lower, "www.")
}

// imageOnlyRequestLabel 给纯图消息一个能读的「原请求」：有缓存的识图描述就用描述，
// 没有就写明是一张图，别让话题判断拿到一个空串。
func (r *Runtime) imageOnlyRequestLabel(ctx context.Context, event MessageEvent) string {
	if !hasImageSegment(event.Segments) {
		return ""
	}
	count := imageSegmentCount(event.Segments)
	label := "（一张图片）"
	if count > 1 {
		label = fmt.Sprintf("（%d 张图片）", count)
	}
	onlyImages := event
	onlyImages.Quoted = nil
	if description := strings.TrimSpace(r.messageImageDescriptionText(ctx, onlyImages)); description != "" {
		return strings.TrimSuffix(label, "）") + "，内容：" + truncateRunes(description, 400) + "）"
	}
	return label
}
