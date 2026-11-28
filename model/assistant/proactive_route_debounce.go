// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
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
// 定时器取走一批之后，评分和生成要好几秒到十几秒。这段时间里同会话来了直接触发的消息，
// 它那一轮看得到这几条还没回（「前面的转发你还没回复」），会一起答掉；评分那一轮却不知道，
// 照样再答一遍。2026-11-09 生产上同一段话 8～90 秒后原样又发一遍就是这么来的。所以取走的
// 那一批登记成 proactiveRouteFlight：直接触发的那一轮把还没开始发的也一并接走，评分那一轮
// 在发送前看到已被接走就不发。已经开始发的不再接走，免得回复只发出半截。
//
// 合并评分里几条话题可能各不相同，整组只给一个结论时，最后那条在跟谁说话会把前面的
// 一起带偏：回放 66 条前一条在叫机器人的连发，整组判最好也只认出 58 条，漏的多是「是的」
// 「我给你举个例子→攻击面管理」这种接机器人话的句子，被后一条 @ 别人带走。所以评分对
// burst="k" 那几条逐条判（burst_k），判为在叫机器人的每条各回一次，见 splitBurstReplies。
const (
	defaultProactiveRouteDebounce       = 3 * time.Second
	maxProactiveRouteMergeWindowSeconds = 120
)

var errProactiveRouteTaken = errors.New("diana: parked proactive route taken by a directly triggered turn")

const (
	proactiveRouteTakenOutcome = "superseded_by_trigger_turn"
	proactiveRouteTakenReason  = "接话评分期间同一会话来了直接叫机器人的消息（@、引用、点名等），这几条已交给那一轮一起回，不再单独发送"
)

// proactiveRouteFlight 是定时器已经取走、还在评分或生成回复的那一批。字段由 routeParkMu 守着。
type proactiveRouteFlight struct {
	items []proactiveReplyCandidate
	// taken：整批（或仅剩的发送者）已被直接触发的那一轮接走。
	taken bool
	// committed：整批回复已经开始往外发，之后不再被接走。
	committed bool
	// takenIDs/committedIDs 按消息 ID 细粒度跟踪跨人同批场景：A 用户的直接触发只接走
	// A 的候选，不阻断同批里 B 用户的独立回复。
	takenIDs     map[string]bool
	committedIDs map[string]bool
}

type proactiveRouteFlightTarget struct {
	flight     *proactiveRouteFlight
	messageIDs []string
}

type proactiveRouteFlightContextKey struct{}

func sameProactiveRouteSender(a, b MessageEvent) bool {
	ua, ub := strings.TrimSpace(a.UserID), strings.TrimSpace(b.UserID)
	return ua != "" && ua == ub
}

func proactiveRouteTargetMessageIDs(target MessageEvent) []string {
	ids := make([]string, 0, 1+len(target.backlogTurn))
	if id := strings.TrimSpace(target.MessageID); id != "" {
		ids = append(ids, id)
	}
	for _, candidate := range target.backlogTurn {
		if id := strings.TrimSpace(candidate.Event.MessageID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

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

// evictedProactiveRouteCandidates 找出被等待批条数上限挤掉的旧候选，把它们的占位原因换成结果说明。
func evictedProactiveRouteCandidates(parked, merged []proactiveReplyCandidate) []proactiveReplyCandidate {
	if len(parked) == 0 {
		return nil
	}
	kept := make(map[string]bool, len(merged))
	for _, item := range merged {
		if id := strings.TrimSpace(item.Event.MessageID); id != "" {
			kept[id] = true
		}
	}
	var evicted []proactiveReplyCandidate
	for _, item := range parked {
		id := strings.TrimSpace(item.Event.MessageID)
		if id != "" && !kept[id] {
			evicted = append(evicted, item)
		}
	}
	return evicted
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
	park := r.routeParks[key]
	var parked []proactiveReplyCandidate
	if park != nil {
		parked = park.items
	}
	merged := mergeProactiveRouteCandidates(parked, candidates)
	evicted := evictedProactiveRouteCandidates(parked, merged)
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
		r.routeParkMu.Unlock()
		r.recordProactiveBatchEvicted(evicted)
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
	r.routeParkMu.Unlock()
	r.recordProactiveBatchEvicted(evicted)
	return true, nil
}

// takeProactiveRoutePark 只取走同一发送者在等待批和尚未发送的评分批次里的候选：
// 同一人刚发了铺垫/转发又立刻 @ 机器人时并进这一轮，避免重复作答；其他群友的等待
// 候选留在原批里照常走接话评分，既不漏判也不把别人（甚至 @ 其他人的）消息揉进当前轮。
func (r *Runtime) takeProactiveRoutePark(event MessageEvent) []proactiveReplyCandidate {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.UserID) == "" {
		return nil
	}
	key := proactiveRouteParkKey(event)
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	var items []proactiveReplyCandidate
	for _, flight := range r.routeFlights[key] {
		if flight.committed {
			continue
		}
		matchedAny := false
		for _, item := range flight.items {
			id := strings.TrimSpace(item.Event.MessageID)
			if id == strings.TrimSpace(event.MessageID) || !sameProactiveRouteSender(item.Event, event) {
				continue
			}
			if flight.committedIDs[id] {
				continue
			}
			matchedAny = true
			if flight.takenIDs == nil {
				flight.takenIDs = map[string]bool{}
			}
			if id != "" {
				flight.takenIDs[id] = true
			}
			items = append(items, item)
		}
		if matchedAny {
			allTaken := len(flight.items) > 0
			for _, item := range flight.items {
				id := strings.TrimSpace(item.Event.MessageID)
				if id == "" || !flight.takenIDs[id] {
					allTaken = false
					break
				}
			}
			if allTaken {
				flight.taken = true
			}
		}
	}
	if park := r.routeParks[key]; park != nil {
		var remaining []proactiveReplyCandidate
		for _, item := range park.items {
			if strings.TrimSpace(item.Event.MessageID) == strings.TrimSpace(event.MessageID) {
				continue
			}
			if sameProactiveRouteSender(item.Event, event) {
				items = append(items, item)
				continue
			}
			remaining = append(remaining, item)
		}
		if len(remaining) == 0 {
			if park.timer != nil {
				park.timer.Stop()
			}
			delete(r.routeParks, key)
		} else {
			park.items = remaining
		}
	}
	if len(items) == 0 {
		return nil
	}
	return mergeProactiveRouteCandidates(nil, items)
}

// beginProactiveRouteFlightLocked 登记定时器刚取走的那一批；调用方持有 routeParkMu。
func (r *Runtime) beginProactiveRouteFlightLocked(key string, items []proactiveReplyCandidate) *proactiveRouteFlight {
	flight := &proactiveRouteFlight{items: items}
	if r.routeFlights == nil {
		r.routeFlights = map[string][]*proactiveRouteFlight{}
	}
	r.routeFlights[key] = append(r.routeFlights[key], flight)
	return flight
}

func (r *Runtime) endProactiveRouteFlight(key string, flight *proactiveRouteFlight) {
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	flights := r.routeFlights[key]
	kept := flights[:0]
	for _, item := range flights {
		if item != flight {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		delete(r.routeFlights, key)
		return
	}
	r.routeFlights[key] = kept
}

func (r *Runtime) proactiveRouteFlightTaken(flight *proactiveRouteFlight) bool {
	if flight == nil {
		return false
	}
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	return flight.taken && !flight.committed
}

func (r *Runtime) proactiveRouteFlightTakenFor(flight *proactiveRouteFlight, target MessageEvent) bool {
	if flight == nil {
		return false
	}
	ids := proactiveRouteTargetMessageIDs(target)
	r.routeParkMu.Lock()
	defer r.routeParkMu.Unlock()
	if flight.committed {
		return false
	}
	if flight.taken {
		return true
	}
	for _, id := range ids {
		if flight.takenIDs[id] && !flight.committedIDs[id] {
			return true
		}
	}
	return false
}

// proactiveRouteTakenBeforeSend 是发送闸门：这一目标已被同发送者直接触发的那一轮接走
// 就不发。没被接走就在同一把锁里记成已开始发送，之后不会再被接走，分几条发的后几条照常发。
// 已经写到外部系统的这一轮不能丢（同 inboundTurnSuperseded），照发。
func (r *Runtime) proactiveRouteTakenBeforeSend(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	sideEffect := hasExternalSideEffect(ctx)
	switch value := ctx.Value(proactiveRouteFlightContextKey{}).(type) {
	case proactiveRouteFlightTarget:
		if value.flight == nil {
			return false
		}
		r.routeParkMu.Lock()
		defer r.routeParkMu.Unlock()
		taken := value.flight.taken && !value.flight.committed
		for _, id := range value.messageIDs {
			if value.flight.takenIDs[id] && !value.flight.committedIDs[id] {
				taken = true
				break
			}
		}
		if taken && !sideEffect {
			return true
		}
		if value.flight.committedIDs == nil {
			value.flight.committedIDs = map[string]bool{}
		}
		for _, id := range value.messageIDs {
			value.flight.committedIDs[id] = true
		}
		allCommitted := len(value.flight.items) > 0
		for _, item := range value.flight.items {
			id := strings.TrimSpace(item.Event.MessageID)
			if id == "" || !value.flight.committedIDs[id] {
				allCommitted = false
				break
			}
		}
		if allCommitted {
			value.flight.committed = true
		}
		return false
	case *proactiveRouteFlight:
		if value == nil {
			return false
		}
		r.routeParkMu.Lock()
		defer r.routeParkMu.Unlock()
		if value.taken && !value.committed && !sideEffect {
			return true
		}
		value.committed = true
		return false
	default:
		return false
	}
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
	for key := range r.routeFlights {
		delete(r.routeFlights, key)
	}
}

// flushProactiveRoutePark 是定时器到点后的那一次评分，评中了就在这里回复。
// 若同一批里多个不同发送者都在找机器人，按发送者拆成各自独立的回复轮次，避免不同人回复串台。
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
	if len(candidates) == 0 || ctx.Err() != nil {
		r.routeParkMu.Unlock()
		return
	}
	flight := r.beginProactiveRouteFlightLocked(key, candidates)
	r.routeParkMu.Unlock()
	defer r.endProactiveRouteFlight(key, flight)
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
		r.recordProactiveBatchIgnored(eligible, event, "ignored")
		return
	}
	targets := proactiveRoutedTargets(event, text)
	r.recordProactiveBatchRoutedTargets(eligible, targets)
	if len(targets) == 1 {
		r.executeProactiveRouteTarget(ctx, latest.Event, targets[0], history, flight)
		return
	}
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(target proactiveReplyCandidate) {
			defer wg.Done()
			defer recoverGoroutinePanic("proactive_route_debounce.target")
			r.executeProactiveRouteTarget(ctx, latest.Event, target, history, flight)
		}(target)
	}
	wg.Wait()
}

func (r *Runtime) executeProactiveRouteTarget(baseCtx context.Context, batchLatest MessageEvent, target proactiveReplyCandidate, history []MessageEvent, flight *proactiveRouteFlight) {
	event, text := target.Event, target.Text
	ctx := r.rebindProactiveRoutedContext(baseCtx, batchLatest, event, history)
	if flight != nil {
		ctx = context.WithValue(ctx, proactiveRouteFlightContextKey{}, proactiveRouteFlightTarget{
			flight:     flight,
			messageIDs: proactiveRouteTargetMessageIDs(event),
		})
	}
	event.replyHistory, event.replyHistoryLoaded = history, true
	markReplyStage(ctx, replyStageDecided, time.Now())
	r.prefetchCrossGroupContext(event)
	if blocked, reason := r.replyFatigueBlocks(ctx, event, text); blocked {
		event.routingReason = reason
		r.record(r.decisionEventRecord(event, text, "ignored_reply_fatigue"))
		r.recordProactiveBatchIgnored(event.backlogTurn, event, "ignored_reply_fatigue")
		return
	}
	// 评分和疲劳判断都要调模型，这几秒里被同发送者直接触发的那一轮接走了就不必再生成。
	if r.proactiveRouteFlightTakenFor(flight, event) {
		r.record(r.decisionEventRecord(event, text, proactiveRouteTakenOutcome))
		for _, candidate := range event.backlogTurn {
			r.record(r.decisionEventRecord(candidate.Event, candidate.Text, proactiveRouteTakenOutcome))
		}
		return
	}
	r.recordProactiveReplyPending(event, text)
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
			event.routingReason = "主动回复在等待并发名额时被取消：" + ctx.Err().Error()
			r.record(r.decisionEventRecord(event, text, "ignored"))
			return
		}
	}
	if _, err := r.replyAndRecord(withInboundReplyTurnContext(ctx, event), event, text, "replied_proactive"); err != nil {
		log.Printf("diana parked proactive reply failed: %v", err)
	}
}

// launchExtraProactiveRouteTargets 在当场评分同时命中多个不同发送者时，为除主目标外
// 的其余发送者各自起独立回复轮次，保证每条消息拥有独立的回复引用、轨迹与事件状态。
func (r *Runtime) launchExtraProactiveRouteTargets(ctx context.Context, batchLatest MessageEvent, targets []proactiveReplyCandidate, history []MessageEvent) {
	if len(targets) == 0 {
		return
	}
	r.mu.RLock()
	running, runCtx := r.running, r.runCtx
	r.mu.RUnlock()
	if !running || runCtx == nil {
		return
	}
	// 继承原 ctx 上的 trace 起点（用于生成 proactive_route_selected 记录），但脱离单条
	// worker 请求的取消生命周期，改由运行时 runCtx 控制。
	baseCtx := runCtx
	if state := debugTraceFromContext(ctx); state != nil {
		baseCtx = context.WithValue(baseCtx, debugTraceContextKey{}, state)
	}
	for _, target := range targets {
		go func(target proactiveReplyCandidate) {
			defer recoverGoroutinePanic("proactive_route_debounce.extra_target")
			r.executeProactiveRouteTarget(baseCtx, batchLatest, target, history, nil)
		}(target)
	}
}

func (r *Runtime) recordProactiveBatchEvicted(evicted []proactiveReplyCandidate) {
	for _, candidate := range evicted {
		event := candidate.Event
		event.routingReason = "等待接话判断期间群消息较多，这条移出等待批，已收入上下文历史"
		r.record(r.decisionEventRecord(event, candidate.Text, "ignored"))
	}
}

// rebindProactiveRoutedContext 在合并评分或积压批次把回复对象切到批里更早的消息时，
// 把原本绑在批尾消息上的调试轨迹、用量归属、出站幂等账本、预算上限和隐私代理一并
// 切到真正要回复的消息上，避免整轮 Agent 调用链和 SSE 进度推送落到批尾未回复消息名下。
func (r *Runtime) rebindProactiveRoutedContext(ctx context.Context, from, to MessageEvent, history []MessageEvent) context.Context {
	if strings.TrimSpace(to.MessageID) == "" || strings.TrimSpace(to.MessageID) == strings.TrimSpace(from.MessageID) {
		return ctx
	}
	ctx = withLLMUsageContext(ctx, to)
	ctx = r.rebindDebugTraceContext(ctx, from, to)
	ctx = withContextBudgetCap(ctx, r.effectiveConfigForEvent(to).MaxContextTokens)
	if probe := to.backlogProbe; probe != nil && strings.TrimSpace(probe.ID) != "" {
		ctx = withOutboundTurn(ctx, probe.ID)
	} else {
		ctx = context.WithValue(ctx, outboundTurnKey{}, (*outboundTurn)(nil))
	}
	if len(history) > 0 || identityPrivacyScopeFromContext(ctx) != nil {
		ctx = r.withIdentityPrivacyContext(ctx, to, history)
	}
	return ctx
}

// recordProactiveReplyPending 在防抖批次评中目标、即将进入 Agent 生成前把事件页的占位
// 原因从「排队等接话判断」切到「处理中」。保持 outcome 为 merged_into_backlog_turn，
// 这样最终回复结果落库时照常覆写 outcome，且不会把晚到的异步写入覆盖掉最终结果。
func (r *Runtime) recordProactiveReplyPending(event MessageEvent, text string) {
	record := r.decisionEventRecord(event, text, "merged_into_backlog_turn")
	record.Decision = "pending"
	record.Handled = false
	if reason := strings.TrimSpace(event.routingReason); reason != "" {
		record.Reason = "已通过接话评分，正在生成回复（" + reason + "）"
	} else {
		record.Reason = "已通过接话评分，正在生成回复"
	}
	r.record(record)
}

// recordProactiveBatchIgnored 把同一批里其余几条的结果也落成「不回」。它们入站时先记了
// 「等待合并」的占位，整批评完决定不接话以后要是不改，事件页里就一直挂着「已合并」，
// 看上去像被并进了哪条回复。recorded 是结论已经记过的那条。
func (r *Runtime) recordProactiveBatchIgnored(batch []proactiveReplyCandidate, recorded MessageEvent, outcome string) {
	for _, candidate := range batch {
		if candidate.Event.MessageID == recorded.MessageID {
			continue
		}
		event := candidate.Event
		event.routingReason = "和这段时间的群消息一起做了接话评分，结论是不接话（原因记在这一批最后那条消息上）"
		r.record(r.decisionEventRecord(event, candidate.Text, outcome))
	}
}

// recordProactiveBatchRouted 是整批评完决定接话时，给 routed 以外那几条换掉「排队等接话判断」
// 的占位：跟着 routed 那一轮一起回的（backlogTurn）指向那条，没被选上的记成不回。
// 不换的话回复和调用链挂在别的消息上，这几条就一直挂着「还在等判断」。
func (r *Runtime) recordProactiveBatchRouted(batch []proactiveReplyCandidate, routed MessageEvent) {
	r.recordProactiveBatchRoutedTargets(batch, []proactiveReplyCandidate{{Event: routed}})
}

func (r *Runtime) recordProactiveBatchRoutedTargets(batch []proactiveReplyCandidate, targets []proactiveReplyCandidate) {
	primarySet := make(map[string]bool, len(targets))
	joinedTo := make(map[string]string)
	targetIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		id := strings.TrimSpace(target.Event.MessageID)
		if id != "" {
			primarySet[id] = true
			targetIDs = append(targetIDs, id)
		}
		for _, candidate := range target.Event.backlogTurn {
			if cid := strings.TrimSpace(candidate.Event.MessageID); cid != "" {
				joinedTo[cid] = id
			}
		}
	}
	targetDesc := strings.Join(targetIDs, "、")
	for _, candidate := range batch {
		id := strings.TrimSpace(candidate.Event.MessageID)
		if primarySet[id] {
			continue
		}
		event := candidate.Event
		if parentID := joinedTo[id]; parentID != "" {
			event.routingReason = proactiveBatchJoinedReason(parentID)
			r.record(r.decisionEventRecord(event, candidate.Text, "merged_into_backlog_turn"))
			continue
		}
		event.routingReason = fmt.Sprintf("和这段时间的群消息一起做了接话评分，这一批选了消息 %s 来回，这条没被选上，不单独回", targetDesc)
		r.record(r.decisionEventRecord(event, candidate.Text, "ignored"))
	}
}

// recordProactiveBatchJoined 是直接叫机器人的消息把等待批接走时，给接走的几条换掉占位。
func (r *Runtime) recordProactiveBatchJoined(batch []proactiveReplyCandidate, target MessageEvent) {
	for _, candidate := range batch {
		event := candidate.Event
		event.routingReason = proactiveBatchJoinedReason(target.MessageID) + "；那条直接叫了机器人，这几条不再单独评分"
		r.record(r.decisionEventRecord(event, candidate.Text, "merged_into_backlog_turn"))
	}
}

func proactiveBatchJoinedReason(targetMessageID string) string {
	return fmt.Sprintf("并进了消息 %s 那一轮一起处理，回没回、回了什么看那条", targetMessageID)
}

func proactiveRoutedTargets(primary MessageEvent, primaryText string) []proactiveReplyCandidate {
	peers := append([]proactiveReplyCandidate(nil), primary.burstPeers...)
	primary.burstPeers = nil
	return append(peers, proactiveReplyCandidate{Event: primary, Text: primaryText})
}

// splitBurstReplies 按逐条判断挑出这一轮要回的消息：burst="k" 那几条判为在跟机器人说话的，加上
// 当前消息放行的。同一个发送者的多条连发合为一轮（以该发送者最新那条为回复对象、其余挂在
// burstTargets）；不同发送者的命中消息拆成各自独立的回复目标（挂在 burstPeers），避免在一轮里
// 靠模型手写拆条和引用 ID 导致回错人或迹线混在一起。判否的不跟进来。
func (r *Runtime) splitBurstReplies(current MessageEvent, currentText string, burst []proactiveReplyCandidate, ratings participationRatings, currentAllowed bool) (MessageEvent, string, []proactiveReplyCandidate, bool) {
	var selected []proactiveReplyCandidate
	var notes []string
	for i, candidate := range burst {
		verdict := ratings.Burst[i+1]
		if verdict.Directed == nil || !*verdict.Directed {
			notes = append(notes, fmt.Sprintf("连发%d 否", i+1))
			continue
		}
		notes = append(notes, fmt.Sprintf("连发%d 是：%s", i+1, strings.TrimSpace(verdict.Reason)))
		selected = append(selected, candidate)
	}
	summary := "；合并评分逐条：" + strings.Join(notes, "，")
	if currentAllowed {
		selected = append(selected, proactiveReplyCandidate{Event: current, Text: currentText})
	}
	if len(selected) == 0 {
		current.routingReason += summary
		return current, currentText, []proactiveReplyCandidate{{Event: current, Text: currentText}}, false
	}

	type senderGroup struct {
		key     string
		lastPos int
		items   []proactiveReplyCandidate
	}
	groupIndex := map[string]int{}
	var groups []senderGroup
	for i, candidate := range selected {
		key := strings.TrimSpace(candidate.Event.UserID)
		if key == "" {
			key = fmt.Sprintf("__anon_%d", i)
		}
		if idx, ok := groupIndex[key]; ok {
			groups[idx].items = append(groups[idx].items, candidate)
			groups[idx].lastPos = i
			continue
		}
		groupIndex[key] = len(groups)
		groups = append(groups, senderGroup{key: key, lastPos: i, items: []proactiveReplyCandidate{candidate}})
	}
	// 按每个发送者组里最新一条的顺序排列，让包含最新选中消息（尤其是 current）的组排在最后作为主返回对象。
	sort.Slice(groups, func(i, j int) bool { return groups[i].lastPos < groups[j].lastPos })
	groupedTargets := make([]proactiveReplyCandidate, 0, len(groups))
	for _, group := range groups {
		primary := group.items[len(group.items)-1]
		event := primary.Event
		if event.MessageID != current.MessageID {
			// 回复对象换成了判为在叫机器人的连发：它是被点名的，不是闲聊插话。
			event.proactiveReply, event.chatInReply, event.routingDirected = true, false, true
			event.routingReason = "合并评分里这条在跟机器人说话"
		}
		event.routingReason += summary
		event.burstSplit = true
		event.burstTargets = append([]proactiveReplyCandidate(nil), group.items[:len(group.items)-1]...)
		groupedTargets = append(groupedTargets, proactiveReplyCandidate{Event: event, Text: primary.Text})
	}
	primary := groupedTargets[len(groupedTargets)-1]
	event := primary.Event
	if len(groupedTargets) > 1 {
		event.burstPeers = append([]proactiveReplyCandidate(nil), groupedTargets[:len(groupedTargets)-1]...)
	}
	return event, primary.Text, []proactiveReplyCandidate{{Event: event, Text: primary.Text}}, true
}
