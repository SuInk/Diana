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
//   - 前一条还没开始生成：直接取代它。前一条走到回复入口时发现自己被取代就收住，
//     由后一条一并回答（pendingEarlierMessage 会让模型先接住前一条）。
//   - 前一条已经在生成：照旧走追发合并，重复、补充、纠正并进去；判成另起一题的，
//     两条都回——不能拿后一条去掐掉一份已经在写的、不同话题的答案。
//   - 前一条已经开始往外发：不动它。半截话比多回一条更糟。
//
// 只在同一个发送者之间取代，不同的人永远各回各的。登记表只存内存：重启后丢了
// 就退回各回各的，顶多多回一条；误拦一条该发的回复才是事故。
const (
	// senderBurstWindow 是「连发」的判定窗口，按两条消息的到达时间算。
	senderBurstWindow = 90 * time.Second
	// senderTurnRetention 是登记项的兜底有效期。正常情况下一轮结束就注销，
	// 这里只清理那些没走到收尾的（过期丢弃、断线回补等）。
	senderTurnRetention = 5 * time.Minute
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
	// supersededBy 是取代它的那条消息 ID。
	supersededBy string
	// mergeCheckedRoot 记下预处理阶段已经拿哪一轮做过话题判断，回复入口不再重复问一遍。
	mergeCheckedRoot string
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
	if r.senderTurnLocked(key, messageID) != nil {
		return
	}
	if r.senderTurns == nil {
		r.senderTurns = map[string][]*senderTurn{}
	}
	r.senderTurnSeq++
	r.senderTurns[key] = append(r.senderTurns[key], &senderTurn{seq: r.senderTurnSeq, event: event, arrivedAt: now})
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
			}
		}
		if len(kept) == 0 {
			delete(r.senderTurns, key)
			continue
		}
		r.senderTurns[key] = kept
	}
}

// finishSenderTurn 在这一轮收尾时注销登记。
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
		if strings.TrimSpace(turn.event.MessageID) != messageID {
			kept = append(kept, turn)
		}
	}
	if len(kept) == 0 {
		delete(r.senderTurns, key)
		return
	}
	r.senderTurns[key] = kept
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

// noteSenderTurnMergeChecked 记下这条消息已经和哪一轮做过话题判断。
func (r *Runtime) noteSenderTurnMergeChecked(event MessageEvent, rootMessageID string) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if turn := r.senderTurnLocked(key, messageID); turn != nil {
		turn.mergeCheckedRoot = strings.TrimSpace(rootMessageID)
	}
}

// enterSenderTurnReply 在回复入口把登记推进到「生成中」。已经被取代的返回取代者；
// 推进和检查在同一把锁里，取代与开始生成不会交错。没登记过的（直接调用回复入口的
// 路径）在这里补登记。
func (r *Runtime) enterSenderTurnReply(event MessageEvent) (string, bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return "", false
	}
	now := time.Now()
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.senderTurnLocked(key, messageID)
	if turn == nil {
		if r.senderTurns == nil {
			r.senderTurns = map[string][]*senderTurn{}
		}
		r.senderTurnSeq++
		turn = &senderTurn{seq: r.senderTurnSeq, event: event, arrivedAt: now}
		r.senderTurns[key] = append(r.senderTurns[key], turn)
	}
	if turn.supersededBy != "" {
		return turn.supersededBy, true
	}
	if turn.stage < senderTurnReplying {
		turn.stage = senderTurnReplying
	}
	// 预处理可能把回复对象换成了另一条（积压合并、主动回复路由），以回复时的事件为准。
	turn.event = event
	return "", false
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

// passSenderTurnSendGate 是发送闸门上的那一步：对话回复已被取代就拦下；否则把它
// 记成「开始发送」。检查和标记在同一把锁里，取代不会插在两者之间。
func (r *Runtime) passSenderTurnSendGate(event MessageEvent, directRun bool) bool {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return true
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.senderTurnLocked(key, messageID)
	if turn == nil {
		return true
	}
	if turn.supersededBy != "" {
		return !directRun
	}
	turn.stage = senderTurnSending
	return true
}

// senderTurnSupersededBy 报告这条消息是否已被同一个人后到的消息取代。
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

// directReplyOutcome 圈出按「对话回复」处理的轮次：只有它们参与追发合并和连发取代。
// 链接解析、插件指令这类确定性输出不在其中——那张卡片丢了没人会补。
func directReplyOutcome(event MessageEvent, successOutcome string) bool {
	return successOutcome == "replied" || successOutcome == "replied_direct_followup" || event.proactiveReply || event.chatInReply
}

// claimSenderBurst 在回复入口处理同一个人的连发。返回 (outcome, true) 表示这一轮
// 到此为止，不再生成。
func (r *Runtime) claimSenderBurst(ctx context.Context, event MessageEvent, text string, successOutcome string) (MessageEvent, string, bool) {
	supersededBy, superseded := r.enterSenderTurnReply(event)
	if !directReplyOutcome(event, successOutcome) {
		return event, "", false
	}
	if superseded {
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
		// 被取代的那几条要真的出现在这一轮的上下文里，否则「一并回答」只是空话。
		// 历史是预处理时取的，前一条要是之后才进历史，就让回复重新取一次。
		if !eventHistoryContainsAll(event.replyHistory, absorbed) {
			event.replyHistory, event.replyHistoryLoaded = nil, false
		}
		ids := make([]string, 0, len(absorbed))
		for _, item := range absorbed {
			ids = append(ids, strings.TrimSpace(item.MessageID))
		}
		log.Printf("diana sender burst: message %s supersedes earlier %s from the same sender", strings.TrimSpace(event.MessageID), strings.Join(ids, ","))
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

// supersedeEarlierSenderTurns 让同一个人更早到、还没开始生成的消息让位给当前这条。
//
// 只取代「看得见」的：前一条得已经进了会话历史，这一轮的模型才答得到它。前一条
// 要是还卡在识图之类的预处理里、历史里还没有，就不取代，两条各回——多回一条
// 总比有一条谁都没回强。
func (r *Runtime) supersedeEarlierSenderTurns(event MessageEvent) []MessageEvent {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return nil
	}
	visible := r.sessionHistoryMessageIDs(event)
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	current := r.senderTurnLocked(key, messageID)
	if current == nil {
		return nil
	}
	var absorbed []MessageEvent
	for _, turn := range r.senderTurns[key] {
		earlierID := strings.TrimSpace(turn.event.MessageID)
		if turn == current || turn.seq > current.seq || turn.stage != senderTurnRouting || turn.supersededBy != "" {
			continue
		}
		if current.arrivedAt.Sub(turn.arrivedAt) > senderBurstWindow || !visible[earlierID] {
			continue
		}
		turn.supersededBy = messageID
		absorbed = append(absorbed, turn.event)
	}
	return absorbed
}

// sessionHistoryMessageIDs 取会话内存历史里已有的消息 ID。
func (r *Runtime) sessionHistoryMessageIDs(event MessageEvent) map[string]bool {
	r.mu.RLock()
	history := r.history[sessionKey(event)]
	ids := make(map[string]bool, len(history))
	for _, item := range history {
		if id := strings.TrimSpace(item.MessageID); id != "" {
			ids[id] = true
		}
	}
	r.mu.RUnlock()
	return ids
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
// 让那条图自己的那一轮让位。#816 去掉图文合并后，「先发图、再问这个呢」是两轮：
// 文字那一轮已经带着图在答了，图那一轮再单独回一遍就是重复。
//
// 只处理纯图（没有文字）的消息，只处理还没开始发送的；内存里标记给发送闸门看，
// 同时写 superseded_by，让持久化的发送前检查（InboundEventSuperseded）也拦得住。
func (r *Runtime) supersedeDependencyImageTurns(ctx context.Context, event MessageEvent, images []senderDependencyImage) {
	key := directReplyMergeKey(event)
	currentID := strings.TrimSpace(event.MessageID)
	if key == "" || currentID == "" || len(images) == 0 {
		return
	}
	seen := map[string]bool{}
	var claimed []MessageEvent
	r.replyInterruptMu.Lock()
	for _, image := range images {
		source := image.Source
		sourceID := strings.TrimSpace(source.MessageID)
		if sourceID == "" || sourceID == currentID || seen[sourceID] || directReplyMergeKey(source) != key {
			continue
		}
		seen[sourceID] = true
		if strings.TrimSpace(historyPlainText(source)) != "" {
			continue
		}
		turn := r.senderTurnLocked(key, sourceID)
		if turn == nil || turn.stage >= senderTurnSending || turn.supersededBy != "" {
			continue
		}
		turn.supersededBy = currentID
		claimed = append(claimed, turn.event)
	}
	r.replyInterruptMu.Unlock()
	if len(claimed) == 0 {
		return
	}
	turnID := currentID
	if turn := outboundTurnFromContext(ctx); turn != nil && strings.TrimSpace(turn.id) != "" {
		turnID = strings.TrimSpace(turn.id)
	}
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundReplyMergeStore)
	r.mu.RUnlock()
	for _, source := range claimed {
		log.Printf("diana sender burst: image message %s answered together with %s", strings.TrimSpace(source.MessageID), currentID)
		if store == nil {
			continue
		}
		mergeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if err := store.RecordInboundEventReplyMerge(mergeCtx, source, turnID); err != nil {
			log.Printf("diana record dependency image supersession failed: %v", err)
		}
		cancel()
	}
}

// senderImageThenShortText 判断「前一条是纯图、后一条是同一个人几十秒内补的一句短话」。
// 这种组合几乎都是在说那张图，按规则直接算补充，不必再问一次话题判断——那次判断
// 拿到的原请求是空的，十有八九判成另起一题。
func senderImageThenShortText(root MessageEvent, rootArrived time.Time, event MessageEvent, eventArrived time.Time, text string) bool {
	if strings.TrimSpace(root.UserID) == "" || strings.TrimSpace(root.UserID) != strings.TrimSpace(event.UserID) {
		return false
	}
	if strings.TrimSpace(historyPlainText(root)) != "" || !pendingEarlierImageOnly(root) {
		return false
	}
	if hasImageSegment(event.Segments) {
		return false
	}
	text = strings.TrimSpace(firstNonEmpty(textSegmentsOnly(event.Segments), text))
	if text == "" || len([]rune(text)) > senderImageFollowUpMaxRunes {
		return false
	}
	gap := eventArrived.Sub(rootArrived)
	if root.Time > 0 && event.Time > 0 {
		gap = time.Duration(event.Time-root.Time) * time.Second
	}
	return gap >= 0 && gap <= senderImageFollowUpWindow
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
