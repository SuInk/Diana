// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// 群里几个人快速你来我往时，每来一条都跑一次接话评分，大半是重复劳动：下一条几秒后就到，
// 评分看的上下文几乎一样。生产 7 天回放（2026-10-22～28，24929 次评分）里，同群下一次
// 评分在 3 秒内开始的占 19.5%，token 占 20%。
//
// 要进接话评分的群消息不当场评：登记进会话的等待批（proactiveRoutePark），定时器到点
// 再把整批一起评一次。等待期间同会话又有候选进来就并进同一批、定时器往后挪；@、引用、
// 叫名字这些直接触发的消息不等，进来时把等待批整个接走，并进自己那一轮（见
// takeProactiveRoutePark）。
//
// 等待不能放在入站 worker 里睡：群会话默认 3 条并发处理，后一条多半已经在处理中，
// 以前那版按「库里还有没有排队的新消息」交接，2026-10-29～11-04 生产上 2 秒内相邻的
// 候选 7514 对只合并上 1174 对；全局 worker 也只有 MaxBotConcurrency 个，窗口一拉长，
// 几个活跃群就能把它们占满，@ 消息跟着排队。所以登记完 worker 立刻收工，评分和回复在
// 定时器的 goroutine 里跑，回复照样占 r.sem 的名额。
//
// 默认只等 defaultProactiveRouteDebounce（安静这么久就评）。接话设置里的「合并判断」可以
// 把等待拉长成窗口：从这一批最早那条算起最多等 merge_window_seconds，攒满 merge_max_messages
// 条立即评。等待批只在内存里，进程重启时还没评的那几条只留在历史里，不再补评。
//
// 合并评分里几条话题可能各不相同，整组只给一个结论时，最后那条在跟谁说话会把前面的
// 一起带偏：回放 66 条前一条在叫机器人的连发，整组判最好也只认出 58 条，漏的多是「是的」
// 「我给你举个例子→攻击面管理」这种接机器人话的句子，被后一条 @ 别人带走。所以评分对
// 【连发k】逐条判（burst_k），判为在叫机器人的每条各回一次，见 splitBurstReplies。
const (
	defaultProactiveRouteDebounce       = 3 * time.Second
	maxProactiveRouteMergeWindowSeconds = 120
)

type proactiveRoutePark struct {
	items      []proactiveReplyCandidate
	generation uint64
	timer      *time.Timer
}

// proactiveRouteParkKey 按会话和机器人分批：同群两台机器人各评各的。
func proactiveRouteParkKey(event MessageEvent) string {
	key := sessionKey(event)
	if profileID := strings.TrimSpace(event.ProfileID); profileID != "" {
		key += "|profile:" + profileID
	}
	return key
}

// proactiveRouteMergeLimits 返回合并判断的窗口和条数上限；maxMessages 为 0 表示不限
// （一批最多 backlogMessageMaxItems 条）。
func proactiveRouteMergeLimits(cfg BotConfig) (window time.Duration, maxMessages int) {
	p := cfg.chatInSettings().Participation
	if p == nil {
		return 0, 0
	}
	window = time.Duration(max(0, min(maxProactiveRouteMergeWindowSeconds, p.MergeWindowSeconds))) * time.Second
	return window, max(0, min(backlogMessageMaxItems, p.MergeMaxMessages))
}

// proactiveRouteCandidateTime 取候选的平台发送时间，缺了退回登记时间。
func proactiveRouteCandidateTime(candidate proactiveReplyCandidate, now time.Time) time.Time {
	if candidate.Event.Time > 0 {
		if at := time.Unix(candidate.Event.Time, 0); at.Before(now) {
			return at
		}
		return now
	}
	if !candidate.QueuedAt.IsZero() && candidate.QueuedAt.Before(now) {
		return candidate.QueuedAt
	}
	return now
}

// proactiveRouteWait 是这一批还要等多久：至少一个防抖，合并窗口没用完就等到窗口结束。
func proactiveRouteWait(debounce, window time.Duration, oldest, now time.Time) time.Duration {
	wait := debounce
	if remaining := window - now.Sub(oldest); remaining > wait {
		wait = remaining
	}
	return wait
}

// mergeProactiveRouteCandidates 把新候选接在等待批后面，同一条消息只留一份，按发送时间排好。
func mergeProactiveRouteCandidates(parked, incoming []proactiveReplyCandidate) []proactiveReplyCandidate {
	seen := make(map[string]bool, len(parked)+len(incoming))
	merged := make([]proactiveReplyCandidate, 0, len(parked)+len(incoming))
	for _, candidate := range append(append([]proactiveReplyCandidate(nil), parked...), incoming...) {
		if id := strings.TrimSpace(candidate.Event.MessageID); id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		merged = append(merged, candidate)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Event.Time < merged[j].Event.Time })
	if len(merged) > backlogMessageMaxItems {
		merged = merged[len(merged)-backlogMessageMaxItems:]
	}
	return merged
}

// parkProactiveRoute 把候选登记进会话的等待批。parked 为 false 时调用方当场评分，
// candidates 是要一起评的整批（等待批里已有的也取出来了）。
func (r *Runtime) parkProactiveRoute(event MessageEvent, candidates []proactiveReplyCandidate) (bool, []proactiveReplyCandidate) {
	if event.Kind != EventKindGroup || event.backlogProbe == nil {
		return false, candidates
	}
	r.mu.RLock()
	runCtx, running, debounce := r.runCtx, r.running, r.proactiveRouteDebounce
	r.mu.RUnlock()
	cfg := r.effectiveConfigForEvent(event)
	key := proactiveRouteParkKey(event)
	now := time.Now()
	for i := range candidates {
		if candidates[i].QueuedAt.IsZero() {
			candidates[i].QueuedAt = now
		}
	}
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	park := r.routeParks[key]
	var parked []proactiveReplyCandidate
	if park != nil {
		parked = park.items
	}
	merged := mergeProactiveRouteCandidates(parked, candidates)
	window, maxMessages := proactiveRouteMergeLimits(cfg)
	// 重试的那条可能已经发出去一半，主人的消息要在自己那一轮生效：都当场评，等着的一起带上。
	routeNow := debounce <= 0 || !running || runCtx == nil || event.backlogProbe.Attempts > 1 || cfg.IsOwnerEvent(event) ||
		(maxMessages > 0 && len(merged) >= maxMessages)
	if routeNow {
		if park != nil {
			if park.timer != nil {
				park.timer.Stop()
			}
			delete(r.routeParks, key)
		}
		return false, merged
	}
	if park == nil {
		park = &proactiveRoutePark{}
		if r.routeParks == nil {
			r.routeParks = map[string]*proactiveRoutePark{}
		}
		r.routeParks[key] = park
	}
	park.items = merged
	park.generation++
	if park.timer != nil {
		park.timer.Stop()
	}
	generation := park.generation
	wait := proactiveRouteWait(debounce, window, proactiveRouteCandidateTime(merged[0], now), now)
	park.timer = time.AfterFunc(wait, func() {
		r.flushProactiveRoutePark(runCtx, key, generation)
	})
	return true, nil
}

// takeProactiveRoutePark 取走会话的等待批：直接触发的消息进来时调用，等着的候选并进它那一轮。
func (r *Runtime) takeProactiveRoutePark(event MessageEvent) []proactiveReplyCandidate {
	if event.Kind != EventKindGroup {
		return nil
	}
	key := proactiveRouteParkKey(event)
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	park := r.routeParks[key]
	if park == nil {
		return nil
	}
	if park.timer != nil {
		park.timer.Stop()
	}
	delete(r.routeParks, key)
	items := make([]proactiveReplyCandidate, 0, len(park.items))
	for _, item := range park.items {
		if item.Event.MessageID != event.MessageID {
			items = append(items, item)
		}
	}
	return items
}

func (r *Runtime) clearProactiveRouteParks() {
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	for key, park := range r.routeParks {
		if park.timer != nil {
			park.timer.Stop()
		}
		delete(r.routeParks, key)
	}
}

// flushProactiveRoutePark 是定时器到点后的那一次评分，评中了就在这里回复。
func (r *Runtime) flushProactiveRoutePark(ctx context.Context, key string, generation uint64) {
	defer recoverGoroutinePanic("proactive_route_debounce.flush")
	r.routeParkMu.Lock()
	park := r.routeParks[key]
	if park == nil || park.generation != generation {
		r.routeParkMu.Unlock()
		return
	}
	delete(r.routeParks, key)
	candidates := park.items
	r.routeParkMu.Unlock()
	if len(candidates) == 0 || ctx.Err() != nil {
		return
	}
	// 等的这几秒里，发送者可能刚被叫停。
	eligible := candidates[:0:0]
	for _, candidate := range candidates {
		if restriction, blocked := r.activeReplySuppression(candidate.Event, time.Now()); blocked {
			r.recordReplySuppressionBlocked(candidate.Event, restriction)
			continue
		}
		eligible = append(eligible, candidate)
	}
	if len(eligible) == 0 {
		return
	}
	latest := eligible[len(eligible)-1]
	ctx = withLLMUsageContext(ctx, latest.Event)
	ctx = r.withDebugTraceContext(ctx, latest.Event)
	ctx = withContextBudgetCap(ctx, r.effectiveConfigForEvent(latest.Event).MaxContextTokens)
	if probe := latest.Event.backlogProbe; probe != nil && strings.TrimSpace(probe.ID) != "" {
		ctx = withOutboundTurn(ctx, probe.ID)
	}
	// 候选不是从入站 worker 一路走过来的，历史按现在重新取，等待期间的新消息也得看见。
	history, _ := r.sessionContextHistory(latest.Event)
	ctx = r.withIdentityPrivacyContext(ctx, latest.Event, history)
	event, text, allowed := r.resolveProactiveRoute(ctx, latest.Event, eligible)
	if !allowed {
		r.record(r.decisionEventRecord(event, text, "ignored"))
		return
	}
	event.replyHistory, event.replyHistoryLoaded = history, true
	markReplyStage(ctx, replyStageDecided, time.Now())
	r.prefetchCrossGroupContext(event)
	if blocked, reason := r.replyFatigueBlocks(ctx, event, text); blocked {
		event.routingReason = reason
		r.record(r.decisionEventRecord(event, text, "ignored_reply_fatigue"))
		return
	}
	r.mu.RLock()
	sem := r.sem
	r.mu.RUnlock()
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
	if _, err := r.replyAndRecord(withInboundReplyTurnContext(ctx, event), event, text, "replied_proactive"); err != nil {
		log.Printf("diana parked proactive reply failed: %v", err)
	}
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
