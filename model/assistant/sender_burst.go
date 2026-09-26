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
// 现在是「交接」：消息一被接手就登记（入站时，早于路由和任何模型调用），按会话+
// 发送者分组。后到那条真正构建回复提示词时（claimCarryOver），把同一个人更早、
// 还卡在路由里的连发接过来——接过来的就是提示词逐条点名承接的那一批，两边永远一致。
// 被接走的那一轮走到回复入口或发送闸门时不等任何人，立刻以 handed_off_pending
// 收尾，让出 worker、全局并发、会话名额和发送锁。
//
// 后到那一轮结束时结算：它确实带着点名承接的提示词生成、并且至少有一条模型回复
// 真的发了出去，交接才落定（superseded_follow_up）；否则（主人命令、链接解析、
// 模型不接话、出错、被拦……）一律撤销，把早的那条重新排进队列自己回答。交接状态
// 写进 inbound_events，进程重启后由巡检放回（见 inbound_handoff.go）。
//
// 几条硬边界：
//   - 只从旧往新接：只接消息时间更早（同一秒按登记先后）的那几条；依赖图也只取当前
//     这条之前的图，不会有两轮互相接走；
//   - 已经被别的轮次接走、或者自己那一轮已经在回答的消息，不点名也不接；
//   - 只在同一个发送者之间，不同的人永远各回各的；
//   - 只管对话回复：链接解析、插件指令的消息既不接别人，也不被接——那张卡片丢了没人会补；
//   - 明确叫机器人（@、引用、叫名字）的前一条，不会被后一条随口的主动接话接走；
//   - 已经开始往外发的那一轮不动。半截话比多回一条更糟。
//
// 登记表只存内存，交接状态另外落库：内存丢了，巡检把待定的交接放回去，最多多回一条；
// 误吞一条该回的消息才是事故。
const (
	// senderBurstWindow 是「连发」的判定窗口，按两条消息的时间算。
	senderBurstWindow = 90 * time.Second
	// senderTurnRetention 是登记项的兜底有效期。正常情况下一轮结束就注销，
	// 这里只清理那些没走到收尾的（过期丢弃、断线回补、已结算的交接等）。
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
	// senderTurnSending：第一条回复已经过了发送闸门，之后不再被接走。
	senderTurnSending
)

type senderTurn struct {
	seq       uint64
	event     MessageEvent
	arrivedAt time.Time
	stage     senderTurnStage
	// chatReply 表示它进回复入口时是一轮对话回复（不是链接解析、插件指令）。
	chatReply bool
	// inboundID 是它在入站队列里的事件 ID；有它、且队列支持交接持久化时，撤销交接
	// 靠重新排队，否则在进程内重新派发。交接落库也按它（主键）定位。
	inboundID string
	// ready 表示预处理（语音转写、图片和文件解析、转发展开）已经做完、进了会话历史。
	ready bool
	// sideEffect 表示这一轮已经在外部系统留下痕迹，它的发送绕过闸门，不能再被接走。
	sideEffect bool

	// 被接走的一方。supersededBy 是接手那条的消息 ID，absorberID 是接手那一轮的
	// 入站事件 ID；final 为假时交接还是待定的。
	supersededBy  string
	absorberID    string
	final         bool
	viaDependency bool
	// handedOff：这一轮已经以 handed_off_pending 收尾；stoppedAtGate：它是在发送闸门
	// 上被拦下的。replay* 是撤销交接后在进程内重新派发要用的东西。
	handedOff     bool
	stoppedAtGate bool
	replayText    string
	replayOutcome string

	// 接手的一方。turnID 是本轮的入站事件 ID（没有就用消息 ID）；covered 是提示词里
	// 确实点名承接了的消息；delivered 是至少有一条模型回复发了出去；live 表示它在
	// liveAbsorbers 里记过数。
	turnID    string
	absorbed  []*senderTurn
	covered   map[string]bool
	delivered bool
	live      bool

	// mergeCheckedRoot 记下预处理阶段已经拿哪一轮做过话题判断，回复入口不再重复问一遍。
	mergeCheckedRoot string
	// notMergedWith 是话题判断认定「不是同一件事」的那几轮，依赖图那条路不再接走它们。
	notMergedWith map[string]bool
}

// pendingHandoff 报告它是不是一条还没落定的交接。
func (turn *senderTurn) pendingHandoff() bool {
	return turn.supersededBy != "" && !turn.final
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

// noteSenderTurnInbound 记下这条消息在入站队列里的事件 ID。
func (r *Runtime) noteSenderTurnInbound(event MessageEvent, inboundID string) {
	key := directReplyMergeKey(event)
	if key == "" || strings.TrimSpace(event.MessageID) == "" || strings.TrimSpace(inboundID) == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	r.ensureSenderTurnLocked(key, event, time.Now()).inboundID = strings.TrimSpace(inboundID)
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

// pruneSenderTurnsLocked 清掉过期的登记。还在跑的轮次（收尾时自己注销）和待定的
// 交接（等接手那一轮结算）不清。
func (r *Runtime) pruneSenderTurnsLocked(now time.Time) {
	for key, turns := range r.senderTurns {
		kept := turns[:0]
		for _, turn := range turns {
			running := turn.stage >= senderTurnReplying && !turn.handedOff
			if now.Sub(turn.arrivedAt) <= senderTurnRetention || running || turn.pendingHandoff() || len(turn.absorbed) > 0 {
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

// finishSenderTurn 在这一轮收尾时注销登记。交出去的那一轮留着，等接手那一轮结算
// （后来的轮次据此知道它已经有人接了），结算后由过期清理带走。
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
		if strings.TrimSpace(turn.event.MessageID) != messageID || turn.handedOff || len(turn.absorbed) > 0 {
			kept = append(kept, turn)
		}
	}
	if len(kept) == 0 {
		delete(r.senderTurns, key)
		return
	}
	r.senderTurns[key] = kept
}

// enterSenderTurnReply 在回复入口把登记推进到「生成中」。没登记过的（直接调用回复
// 入口的路径）在这里补登记。撤销交接后重新跑的那一轮从这里重新开始。
func (r *Runtime) enterSenderTurnReply(event MessageEvent, chatReply bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.ensureSenderTurnLocked(key, event, time.Now())
	turn.handedOff, turn.stoppedAtGate = false, false
	if turn.stage < senderTurnReplying {
		turn.stage = senderTurnReplying
	}
	turn.chatReply = chatReply
	// 预处理可能把回复对象换成了另一条（积压合并、主动回复路由），以回复时的事件为准。
	turn.event = event
}

// noteSenderTurnReady 在消息预处理完、进了会话历史之后调用：从这一刻起它才能被接走。
func (r *Runtime) noteSenderTurnReady(event MessageEvent) {
	key := directReplyMergeKey(event)
	if key == "" || strings.TrimSpace(event.MessageID) == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	r.ensureSenderTurnLocked(key, event, time.Now()).ready = true
}

// preprocessedForCarryOver 看历史里这条消息本身是不是已经能读：语音得有转写。
// 这是 ready 之外的一道兜底，防的是转写失败或禁言期间跳过转写的语音。
func preprocessedForCarryOver(event MessageEvent) bool {
	for _, segment := range event.Segments {
		if segment.Type == "record" && strings.TrimSpace(segment.Data[voiceSTTTranscriptKey]) == "" {
			return false
		}
	}
	return true
}

// markSenderTurnSideEffect 在这一轮已经写到外部系统、之后的发送绕过闸门时调用。
// 它的回复一定会发出去，所以不能再算作交出去了：还没落定的交接就地撤销（接手那一轮
// 结算时看到它已经不归自己，不落定也不重排；落库的待定标记由巡检清掉）。
func (r *Runtime) markSenderTurnSideEffect(event MessageEvent) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.senderTurnLocked(key, messageID)
	if turn == nil {
		return
	}
	turn.sideEffect, turn.stage = true, senderTurnSending
	if turn.pendingHandoff() {
		turn.supersededBy, turn.absorberID, turn.viaDependency = "", "", false
	}
}

// passSenderTurnSendGate 是发送闸门上的那一步，只看状态、从不等待（它可能正握着
// 这个人的发送锁）。对话回复已经交给后一条就拦下；否则在同一把锁里记成「开始发送」。
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
		if !directRun {
			// 链接解析、插件指令的产出不受交接影响。
			return true
		}
		turn.stoppedAtGate = true
		return false
	}
	turn.stage = senderTurnSending
	return true
}

type carryOverDeliveryExcludedKey struct{}

// withoutCarryOverDelivery 标记「这次发送不是模型对这条消息的回答」（错误提示之类），
// 它发出去不算接手那一轮回复了前几条。
func withoutCarryOverDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, carryOverDeliveryExcludedKey{}, true)
}

// noteSenderTurnDelivered 在一条模型回复真的发出去之后调用，供接手那一轮结算。
func (r *Runtime) noteSenderTurnDelivered(ctx context.Context, event MessageEvent) {
	if !replyTriggerGateEnabled(ctx) {
		return
	}
	if excluded, _ := ctx.Value(carryOverDeliveryExcludedKey{}).(bool); excluded {
		return
	}
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if turn := r.senderTurnLocked(key, messageID); turn != nil {
		turn.delivered = true
	}
}

// senderTurnSupersededBy 报告这条消息是否已经交给同一个人后到的消息（含待定）。
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

// handOffSenderTurn 在回复入口（stopped=false）或一轮跑完之后（stopped=true）检查
// 这一轮是不是已经交给后一条。是就把它记成「已交出」，返回收尾结果；redispatch 为真
// 表示交接在它被拦下之后又撤销了、且没有队列可以重排，调用方要在进程内重新派发它。
func (r *Runtime) handOffSenderTurn(event MessageEvent, text, successOutcome string, stopped bool) (outcome string, handed bool, redispatch bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return "", false, false
	}
	durableStore := r.inboundHandoffStore() != nil
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.senderTurnLocked(key, messageID)
	if turn == nil {
		return "", false, false
	}
	// 一轮跑完时已经发出过模型回复、或者在外部系统留下了痕迹：它不是交出去的，
	// 不能记成 handed_off_pending——否则接手那一轮没回出去时它会被重新排队，
	// 出站幂等账本已经清掉，工具和回复会再来一遍。还没落定的交接就地撤销。
	if stopped && (turn.delivered || turn.sideEffect) {
		if turn.pendingHandoff() {
			turn.supersededBy, turn.absorberID, turn.viaDependency = "", "", false
		}
		return "", false, false
	}
	switch {
	case turn.supersededBy != "" && turn.final:
		turn.handedOff = true
		return "superseded_follow_up", true, false
	case turn.supersededBy != "":
		turn.handedOff, turn.replayText, turn.replayOutcome = true, text, successOutcome
		return inboundOutcomeHandedOffPending, true, false
	case stopped && turn.stoppedAtGate:
		// 在闸门上被拦下之后，交接又被撤销了：这一轮的回复已经丢了，得重新回答。
		turn.handedOff, turn.replayText, turn.replayOutcome = true, text, successOutcome
		return inboundOutcomeHandedOffPending, true, !(durableStore && turn.inboundID != "")
	}
	return "", false, false
}

// handedOffState 供入站 worker 收尾：这条消息的交接现在是什么状态。still 为假表示
// 交接已经撤销（或者登记已经没了），该把它重新排进队列。
func (r *Runtime) handedOffState(event MessageEvent) (absorberID string, final bool, still bool) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	turn := r.senderTurnLocked(key, messageID)
	if turn == nil || turn.supersededBy == "" {
		return "", false, false
	}
	return turn.absorberID, turn.final, true
}

// senderTurnID 是接手那一轮的入站事件 ID：交接落库、巡检都按它认人。
func senderTurnID(ctx context.Context, event MessageEvent) string {
	if turn := outboundTurnFromContext(ctx); turn != nil && strings.TrimSpace(turn.id) != "" {
		return strings.TrimSpace(turn.id)
	}
	return strings.TrimSpace(event.MessageID)
}

// burstEarlier 判断 earlier 是否确实早于 later：两边都有平台时间戳就按时间戳，同一
// 秒再按登记先后；回放、断线回补的登记顺序不可信，只在时间相同时才看。
func burstEarlier(earlier *senderTurn, earlierEvent MessageEvent, later *senderTurn, laterEvent MessageEvent) bool {
	if earlierEvent.Time > 0 && laterEvent.Time > 0 && earlierEvent.Time != laterEvent.Time {
		return earlierEvent.Time < laterEvent.Time
	}
	return earlier.seq < later.seq
}

// senderBurstGap 算两条消息的间隔：两边都有平台时间戳就按时间戳，否则按登记的到达时间。
func senderBurstGap(earlier *senderTurn, earlierEvent MessageEvent, later *senderTurn, laterEvent MessageEvent) time.Duration {
	if earlierEvent.Time > 0 && laterEvent.Time > 0 {
		return time.Duration(laterEvent.Time-earlierEvent.Time) * time.Second
	}
	return later.arrivedAt.Sub(earlier.arrivedAt)
}

// absorbLocked 把 turn 交给 current，并把 current 记进 liveAbsorbers。
func (r *Runtime) absorbLocked(current, turn *senderTurn, viaDependency bool) {
	turn.supersededBy = strings.TrimSpace(current.event.MessageID)
	turn.absorberID = current.turnID
	turn.final, turn.viaDependency = false, viaDependency
	current.absorbed = append(current.absorbed, turn)
	if current.covered == nil {
		current.covered = map[string]bool{}
	}
	current.covered[strings.TrimSpace(turn.event.MessageID)] = true
	if !current.live {
		current.live = true
		if r.liveAbsorbers == nil {
			r.liveAbsorbers = map[string]int{}
		}
		r.liveAbsorbers[current.turnID]++
	}
}

// markHandoffs 把刚接过来的交接写进入站队列。
func (r *Runtime) markHandoffs(ctx context.Context, absorberID string, refs []InboundHandoffRef) {
	store := r.inboundHandoffStore()
	if store == nil {
		return
	}
	for _, ref := range refs {
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if err := store.MarkInboundHandoff(markCtx, ref, absorberID); err != nil {
			log.Printf("diana record inbound handoff failed: %v", err)
		}
		cancel()
	}
}

// claimCarryOver 在构建回复提示词时决定这一轮要点名承接哪些消息，并把其中还卡在
// 路由里的接过来。返回的就是提示词逐条点名的那一批：从当前消息往前、同一个人连着发、
// 机器人还没回的消息，去掉已经被别的轮次接走的和自己那一轮正在回答的。
func (r *Runtime) claimCarryOver(ctx context.Context, event MessageEvent, history []MessageEvent) []MessageEvent {
	pending := pendingEarlierMessages(history, event, pendingEarlierMessagesLimit)
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if len(pending) == 0 || key == "" || messageID == "" {
		return pending
	}
	// 随口的主动接话不接明确叫机器人的消息；链接解析、插件指令不参与——当前这条
	// 是链接或插件指令时，它也不接别人。
	laterDirected := !event.proactiveReply && !event.chatInReply
	absorbing := r.burstChatMessage(event, directedInboundText(event))
	eligible := make(map[string]bool, len(pending))
	for _, item := range pending {
		if absorbing && r.burstChatMessage(item, directedInboundText(item)) && (laterDirected || !r.burstMessageDirected(item)) {
			eligible[strings.TrimSpace(item.MessageID)] = true
		}
	}
	turnID := senderTurnID(ctx, event)
	var carry []MessageEvent
	var newly []InboundHandoffRef
	r.replyInterruptMu.Lock()
	current := r.ensureSenderTurnLocked(key, event, time.Now())
	if current.turnID == "" {
		current.turnID = turnID
	}
	for _, item := range pending {
		earlierID := strings.TrimSpace(item.MessageID)
		turn := r.senderTurnLocked(key, earlierID)
		switch {
		case turn == nil:
			// 没有在跑的轮次：它早就收尾了（没回），照旧点名承接。
			carry = append(carry, item)
		case turn == current:
		case turn.supersededBy == messageID:
			carry = append(carry, item)
		case turn.supersededBy != "", turn.stage != senderTurnRouting:
			// 已经交给别的轮次，或者它自己那一轮正在回答：不点名，免得答两遍。
		case !eligible[earlierID] || !burstEarlier(turn, item, current, event):
		case !turn.ready || !preprocessedForCarryOver(item):
			// 还没预处理完（语音在转写、图和文件在解析、转发还没展开）：这时点名只能
			// 点到「[语音]」这样的占位，接过来等于把真正的内容吞了。留给它自己那一轮。
		default:
			if gap := senderBurstGap(turn, item, current, event); gap < 0 || gap > senderBurstWindow {
				continue
			}
			r.absorbLocked(current, turn, false)
			carry = append(carry, item)
			newly = append(newly, InboundHandoffRef{ID: turn.inboundID, Event: turn.event})
		}
	}
	r.replyInterruptMu.Unlock()
	if len(newly) > 0 {
		ids := make([]string, 0, len(newly))
		for _, item := range newly {
			ids = append(ids, strings.TrimSpace(item.Event.MessageID))
		}
		log.Printf("diana sender burst: message %s takes over earlier %s from the same sender", messageID, strings.Join(ids, ","))
		r.markHandoffs(ctx, current.turnID, newly)
	}
	return carry
}

// supersedeDependencyImageTurns 在回复把同一个人刚发的纯图消息当候选依赖图附上时，
// 接过图那一轮。#816 去掉图文合并后，「先发图、再问这个呢」是两轮：文字那一轮已经
// 带着图在答了，图那一轮再单独回一遍就是重复。依赖图块逐张标明了来源消息，就是这里
// 的点名承接。
//
// 只接更早的、纯图、还没开始发送的；话题判断已经认定「不是同一件事」的不接；随口的
// 主动接话不接明确叫机器人的图。
func (r *Runtime) supersedeDependencyImageTurns(ctx context.Context, event MessageEvent, images []senderDependencyImage) {
	key := directReplyMergeKey(event)
	currentID := strings.TrimSpace(event.MessageID)
	if key == "" || currentID == "" || len(images) == 0 {
		return
	}
	if !r.burstChatMessage(event, directedInboundText(event)) {
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
	turnID := senderTurnID(ctx, event)
	var newly []InboundHandoffRef
	r.replyInterruptMu.Lock()
	current := r.ensureSenderTurnLocked(key, event, time.Now())
	if current.turnID == "" {
		current.turnID = turnID
	}
	for _, source := range sources {
		sourceID := strings.TrimSpace(source.MessageID)
		if current.notMergedWith[sourceID] {
			continue
		}
		turn := r.senderTurnLocked(key, sourceID)
		if turn == nil || turn == current || turn.stage >= senderTurnSending || turn.supersededBy != "" || turn.handedOff {
			continue
		}
		// 已经在外部系统留下痕迹的一轮（它的发送绕过闸门）不接；自己已经接了别人的也不接，
		// 否则接手链 A←B←C 里 C 没回出去时，A 会被放回来再答一遍。
		if turn.sideEffect || len(turn.absorbed) > 0 {
			continue
		}
		if turn.stage == senderTurnReplying && !turn.chatReply {
			continue
		}
		if !burstEarlier(turn, source, current, event) {
			continue
		}
		r.absorbLocked(current, turn, true)
		newly = append(newly, InboundHandoffRef{ID: turn.inboundID, Event: turn.event})
	}
	r.replyInterruptMu.Unlock()
	for _, source := range newly {
		log.Printf("diana sender burst: image message %s taken over by %s", strings.TrimSpace(source.Event.MessageID), currentID)
	}
	r.markHandoffs(ctx, current.turnID, newly)
}

// settleSenderBurst 在接手那一轮结束时结算它接过来的交接：带着点名承接的提示词生成、
// 而且模型回复真的发出去了，落定；否则撤销，让那几条自己回答。
func (r *Runtime) settleSenderBurst(ctx context.Context, event MessageEvent) {
	key := directReplyMergeKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	type settled struct {
		event      MessageEvent
		text       string
		outcome    string
		handedOff  bool
		inboundID  string
		absorberID string
	}
	var finalized, released []settled
	r.replyInterruptMu.Lock()
	current := r.senderTurnLocked(key, messageID)
	if current == nil {
		r.replyInterruptMu.Unlock()
		return
	}
	for _, turn := range current.absorbed {
		if turn.supersededBy != messageID || turn.final {
			continue
		}
		item := settled{event: turn.event, text: turn.replayText, outcome: turn.replayOutcome, handedOff: turn.handedOff, inboundID: turn.inboundID, absorberID: turn.absorberID}
		if current.delivered && current.covered[strings.TrimSpace(turn.event.MessageID)] {
			turn.final = true
			finalized = append(finalized, item)
			continue
		}
		turn.supersededBy, turn.absorberID, turn.viaDependency = "", "", false
		released = append(released, item)
	}
	current.absorbed = nil
	wasLive, turnID := current.live, current.turnID
	current.live = false
	r.replyInterruptMu.Unlock()

	store := r.inboundHandoffStore()
	requeued := false
	for _, item := range finalized {
		if store != nil {
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			if err := store.FinalizeInboundHandoff(finalCtx, InboundHandoffRef{ID: item.inboundID, Event: item.event}, item.absorberID); err != nil {
				log.Printf("diana finalize inbound handoff failed: %v", err)
			}
			cancel()
		}
		if item.handedOff && (store == nil || item.inboundID == "") {
			record := r.decisionEventRecord(item.event, item.text, "superseded_follow_up")
			record.Reason = fmt.Sprintf("同一用户随后又发来消息（%s），由那一轮一并回答", messageID)
			r.record(record)
		}
	}
	for _, item := range released {
		log.Printf("diana sender burst: message %s did not answer %s; handing it back", messageID, strings.TrimSpace(item.event.MessageID))
		if store != nil {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, again, err := store.ReleaseInboundHandoff(releaseCtx, InboundHandoffRef{ID: item.inboundID, Event: item.event}, item.absorberID)
			cancel()
			if err != nil {
				log.Printf("diana release inbound handoff failed: %v", err)
			}
			requeued = requeued || again
		}
		if item.handedOff && (store == nil || item.inboundID == "") {
			r.redispatchHandedOff(item.event, item.text, item.outcome)
		}
	}
	if requeued {
		r.wakeInboundWorkers()
	}
	if wasLive {
		r.replyInterruptMu.Lock()
		if r.liveAbsorbers[turnID] <= 1 {
			delete(r.liveAbsorbers, turnID)
		} else {
			r.liveAbsorbers[turnID]--
		}
		r.replyInterruptMu.Unlock()
	}
}

// redispatchHandedOff 在进程内重新回答一条交接被撤销的消息（没有入站队列可以重排时）。
func (r *Runtime) redispatchHandedOff(event MessageEvent, text, successOutcome string) {
	r.mu.RLock()
	ctx, sem := r.runCtx, r.sem
	r.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer recoverGoroutinePanic("sender_burst.redispatchHandedOff")
		if sem != nil {
			select {
			case sem <- struct{}{}:
				r.incActive(1)
				defer func() {
					<-sem
					r.incActive(-1)
				}()
			case <-ctx.Done():
				return
			}
		}
		_, _ = r.replyAndRecord(ctx, event, text, successOutcome)
	}()
}

// claimSenderBurst 在回复入口处理同一个人的连发。返回 (outcome, true) 表示这一轮
// 到此为止，不再生成。
func (r *Runtime) claimSenderBurst(ctx context.Context, event MessageEvent, text string, successOutcome string) (MessageEvent, string, bool) {
	chat := directReplyOutcome(event, successOutcome) && r.burstChatMessage(event, text)
	r.enterSenderTurnReply(event, chat)
	if !chat {
		return event, "", false
	}
	if outcome, handed, _ := r.handOffSenderTurn(event, text, successOutcome, false); handed {
		r.recordHandedOff(event, text, outcome)
		return event, outcome, true
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
	return event, "", false
}

func (r *Runtime) recordHandedOff(event MessageEvent, text, outcome string) {
	supersededBy, _ := r.senderTurnSupersededBy(event)
	record := r.decisionEventRecord(event, text, outcome)
	if outcome == inboundOutcomeHandedOffPending {
		record.Reason = fmt.Sprintf("同一用户随后又发来消息（%s），交给那一轮一并回答；那一轮没回出去的话，这条会重新回答", firstNonEmpty(supersededBy, "后一条"))
	} else {
		record.Reason = fmt.Sprintf("同一用户随后又发来消息（%s），由那一轮一并回答", firstNonEmpty(supersededBy, "后一条"))
	}
	record.Error = ""
	r.record(record)
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

// directReplyOutcome 圈出走追发合并（beginDirectReply）的轮次。
func directReplyOutcome(event MessageEvent, successOutcome string) bool {
	return successOutcome == "replied" || successOutcome == "replied_direct_followup" || event.proactiveReply || event.chatInReply
}

// burstChatMessage 判断这条消息是不是一轮对话回复：shouldHandle 对链接解析、插件
// 指令和主人命令（含编码任务确认码）同样返回 "replied"，得把它们单独剔出去——
// 它们必须在自己那一轮生效，回复又是固定内容，接不住别人的问题。
func (r *Runtime) burstChatMessage(event MessageEvent, text string) bool {
	text = firstNonEmpty(strings.TrimSpace(text), directedInboundText(event))
	return !r.shouldHandleResolver(event, text) && !r.shouldHandlePlugin(event, text) && !r.wouldHandleOwnerCommand(event, text)
}

// burstMessageDirected 判断这条消息是不是明确在叫机器人（@、引用、叫名字、私聊）。
func (r *Runtime) burstMessageDirected(event MessageEvent) bool {
	return r.shouldHandleChatTrigger(event, directedInboundText(event)) || explicitlyRepliesToBot(event, r.effectiveConfigForEvent(event))
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
