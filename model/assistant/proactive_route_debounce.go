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

// 群里几个人快速你来我往时，每来一条都跑一次接话评分，大半是重复劳动：下一条几秒后就到，
// 评分看的上下文几乎一样。生产 7 天回放（2026-10-22～28，24929 次评分）里，同群下一次
// 评分在 3 秒内开始的占 19.5%，token 占 20%。
//
// 做法沿用积压合并：要进接话评分的消息先等 proactiveRouteDebounce，期间同会话又有消息
// 排队，就把这条（连同它之前攒下的候选）登记进积压包，交给后面那条一起评一次。
// 同会话的消息本来就是串行处理的，后面那条一定会来取。@、引用、叫名字这些直接触发的
// 不走这里，不多等。
//
// 合并评分里几条话题可能各不相同，整组只给一个结论时，最后那条在跟谁说话会把前面的
// 一起带偏：回放 66 条前一条在叫机器人的连发，整组判最好也只认出 58 条，漏的多是「是的」
// 「我给你举个例子→攻击面管理」这种接机器人话的句子，被后一条 @ 别人带走。所以评分对
// 【连发k】逐条判（burst_k），判为在叫机器人的每条各回一次，见 splitBurstReplies。
const defaultProactiveRouteDebounce = 3 * time.Second

// deferProactiveRouteToNewer 等一个防抖窗口，报告同会话是不是有更新的消息在排队。
func (r *Runtime) deferProactiveRouteToNewer(ctx context.Context, event MessageEvent) bool {
	wait := r.proactiveRouteDebounce
	if wait <= 0 || event.Kind != EventKindGroup || event.backlogProbe == nil {
		return false
	}
	item := *event.backlogProbe
	// 重试的那条可能已经发出去一半，主人的消息要在自己那一轮生效，都和积压交接同一口径。
	if item.Attempts > 1 || r.effectiveConfigForEvent(event).IsOwnerEvent(event) {
		return false
	}
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundBacklogStore)
	r.mu.RUnlock()
	if store == nil {
		return false
	}
	newer := func() bool {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		pending, err := store.InboundSessionHasNewerPending(checkCtx, item)
		if err != nil {
			log.Printf("diana proactive route debounce check failed: %v", err)
			return false
		}
		return pending
	}
	// 已经有人排着就不用再等。
	if newer() {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false
	}
	return newer()
}

// holdProactiveRouteForNewer 把这条和它之前攒下的候选按原顺序放回积压包。
func (r *Runtime) holdProactiveRouteForNewer(event MessageEvent, text string, now time.Time) {
	key := sessionKey(event)
	held := make([]backlogMessage, 0, len(event.backlogProactive))
	for _, candidate := range event.backlogProactive {
		held = append(held, backlogMessage{proactiveReplyCandidate: candidate, Proactive: true})
	}
	if len(held) > 0 {
		r.backlogMu.Lock()
		if r.backlogMessages == nil {
			r.backlogMessages = map[string][]backlogMessage{}
		}
		items := append(r.backlogMessages[key], held...)
		if len(items) > backlogMessageMaxItems {
			items = items[len(items)-backlogMessageMaxItems:]
		}
		r.backlogMessages[key] = items
		r.backlogMu.Unlock()
	}
	event.backlogProactive = nil
	r.holdBacklogMessage(event, text, false, now)
}

// splitBurstReplies 按逐条判断挑出这一轮要回的消息：【连发k】判为在跟机器人说话的，加上
// 当前消息放行的。仍是一轮回复，最晚那条作回复对象，其余跟着这一轮；话题各自独立，
// 回复按话题分开发送、各自引用原消息（见 backlogReplyRequestText）。判否的不跟进来。
func (r *Runtime) splitBurstReplies(current MessageEvent, currentText string, burst []proactiveReplyCandidate, ratings participationRatings, currentAllowed bool) (MessageEvent, string, []proactiveReplyCandidate, bool) {
	var targets []proactiveReplyCandidate
	var notes []string
	for i, candidate := range burst {
		verdict := ratings.Burst[i+1]
		if verdict.Directed == nil || !*verdict.Directed {
			notes = append(notes, fmt.Sprintf("连发%d 否", i+1))
			continue
		}
		notes = append(notes, fmt.Sprintf("连发%d 是：%s", i+1, strings.TrimSpace(verdict.Reason)))
		targets = append(targets, candidate)
	}
	summary := "；合并评分逐条：" + strings.Join(notes, "，")
	if currentAllowed {
		targets = append(targets, proactiveReplyCandidate{Event: current, Text: currentText})
	}
	if len(targets) == 0 {
		current.routingReason += summary
		return current, currentText, []proactiveReplyCandidate{{Event: current, Text: currentText}}, false
	}
	primary := targets[len(targets)-1]
	event := primary.Event
	if !currentAllowed {
		// 回复对象换成了判为在叫机器人的连发：它是被点名的，不是闲聊插话。
		event.proactiveReply, event.chatInReply, event.routingDirected = true, false, true
		event.routingReason = "合并评分里这条在跟机器人说话"
	}
	event.routingReason += summary
	event.burstSplit = true
	event.burstTargets = targets[:len(targets)-1]
	return event, primary.Text, []proactiveReplyCandidate{{Event: event, Text: primary.Text}}, true
}
