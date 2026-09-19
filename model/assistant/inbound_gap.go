// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 断线回补有没有真的补上，光看「接口没报错」判断不了：QQ 刚登录时离线消息还没从服务器
// 同步到本地，历史接口会老老实实返回「没有新消息」，回补照样显示完成，漏掉的消息就再也
// 不会被补。这里用两件事兜底：
//
//  1. QQ 群消息的 message_seq 逐条递增。重连后每个群收到第一条实时消息时，拿它的 seq
//     跟库里这个群上一条的 seq 比，中间缺的号（扣掉机器人自己发的）就是没补上的消息：
//     针对这个群重补，隔一阵再复查，直到补齐或者放弃并留下日志。
//  2. 私聊没有可靠的 seq，断线后群里也可能再没人说话、等不到那条实时消息，所以重连后
//     再按断线窗口补跑几轮整体回补。
var (
	// historyFollowUpDelays 是重连后整体补跑的时间点（相对连上的时刻）。
	historyFollowUpDelays = []time.Duration{time.Minute, 5 * time.Minute}
	// seqGapRetryDelays 是发现 seq 缺口后每次针对性回补之后等多久再复查；
	// 用完仍有缺口就记一条 backfill_gap_unresolved。
	seqGapRetryDelays = []time.Duration{15 * time.Second, time.Minute, 3 * time.Minute}
)

const seqGapStoreTimeout = 5 * time.Second

// GroupSeqGapQuery 描述一次 seq 缺口检查：Seq/EventTime 是重连后收到的实时消息，
// Since 限定往回找上一条消息的最早时间。
type GroupSeqGapQuery struct {
	ProfileID string
	GroupID   string
	SelfID    string
	Seq       int64
	EventTime int64
	Since     int64
}

// GroupSeqGap 是检查结果。Known 为 false 表示库里找不到可比较的上一条消息，无法判断。
type GroupSeqGap struct {
	Known        bool
	PreviousSeq  int64
	PreviousTime int64
	// SelfMessages 是两条消息之间机器人自己发的消息数。它们同样占用 seq，但本地记录里
	// 不带 seq，只能按条数扣掉。
	SelfMessages int
	Missing      int
}

// InboundSeqGapStore 是可选能力：存储能按群查 seq 缺口时，重连后才做缺口检测。
type InboundSeqGapStore interface {
	GroupSeqGap(ctx context.Context, query GroupSeqGapQuery) (GroupSeqGap, error)
}

type groupSeqProbe struct {
	event MessageEvent
	seq   int64
}

// historyBackfillStats 汇总一次回补拉到和新入库的消息数，写进完成日志，方便判断到底补没补到。
type historyBackfillStats struct {
	Sessions int
	Fetched  int
	Inserted int
}

func (s historyBackfillStats) metadata() map[string]any {
	return map[string]any{
		"sessions": s.Sessions,
		"fetched":  s.Fetched,
		"inserted": s.Inserted,
	}
}

func parseMessageSeq(value string) (int64, bool) {
	seq, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seq <= 0 {
		return 0, false
	}
	return seq, true
}

func groupSeqProbeKey(event MessageEvent) string {
	return event.ProfileID + "\x00" + event.GroupID
}

// armGroupSeqProbes 在每次（重新）连上时调用：清空已探测记录，让每个群的下一条实时消息
// 都复查一次 seq 缺口。
func (r *Runtime) armGroupSeqProbes() {
	r.seqProbeMu.Lock()
	r.seqProbeArmed = true
	r.seqProbed = map[string]struct{}{}
	r.seqProbeMu.Unlock()
}

// observeLiveGroupSeq 在实时群消息入队后调用，每个群每次连接只探测第一条。
func (r *Runtime) observeLiveGroupSeq(event MessageEvent) {
	if event.Kind != EventKindGroup || event.GroupID == "" || NormalizePlatformID(event.Platform) != PlatformOneBotV11 {
		return
	}
	seq, ok := parseMessageSeq(event.MessageSeq)
	if !ok || r.isSelfMessage(event) {
		return
	}
	key := groupSeqProbeKey(event)
	r.seqProbeMu.Lock()
	if !r.seqProbeArmed {
		r.seqProbeMu.Unlock()
		return
	}
	if _, probed := r.seqProbed[key]; probed {
		r.seqProbeMu.Unlock()
		return
	}
	r.seqProbed[key] = struct{}{}
	r.seqProbeMu.Unlock()
	select {
	case r.inboundSeqProbe <- groupSeqProbe{event: event, seq: seq}:
	default:
		// 探测队列满了：放掉这条，让这个群的下一条实时消息再试。
		r.seqProbeMu.Lock()
		delete(r.seqProbed, key)
		r.seqProbeMu.Unlock()
	}
}

// startGroupSeqGapCheck 为一个群起一个缺口复查协程；同一个群同时只跑一个。
func (r *Runtime) startGroupSeqGapCheck(ctx context.Context, store InboundEventStore, probe groupSeqProbe, wg *sync.WaitGroup) {
	gapStore, ok := store.(InboundSeqGapStore)
	if !ok {
		return
	}
	key := groupSeqProbeKey(probe.event)
	r.seqProbeMu.Lock()
	if r.seqGapRunning == nil {
		r.seqGapRunning = map[string]struct{}{}
	}
	if _, running := r.seqGapRunning[key]; running {
		r.seqProbeMu.Unlock()
		return
	}
	r.seqGapRunning[key] = struct{}{}
	r.seqProbeMu.Unlock()
	r.seqGapActive.Add(1)
	wg.Add(1)
	go func() {
		defer recoverGoroutinePanic("inbound_gap.verify")
		defer wg.Done()
		defer func() {
			r.seqProbeMu.Lock()
			delete(r.seqGapRunning, key)
			r.seqProbeMu.Unlock()
			r.seqGapActive.Add(-1)
		}()
		r.verifyGroupSeqGap(ctx, store, gapStore, probe)
	}()
}

func (r *Runtime) verifyGroupSeqGap(ctx context.Context, store InboundEventStore, gapStore InboundSeqGapStore, probe groupSeqProbe) {
	event := probe.event
	query := GroupSeqGapQuery{
		ProfileID: event.ProfileID,
		GroupID:   event.GroupID,
		SelfID:    firstNonEmpty(r.oneBotBotAccount(), r.profileConfig(event.ProfileID).BotAccount, event.SelfID),
		Seq:       probe.seq,
		EventTime: event.Time,
		Since:     event.Time - int64(InboundReplayWindow/time.Second),
	}
	var detected GroupSeqGap
	for attempt := 0; ; attempt++ {
		// 整体回补还在跑时，它可能正要把缺的消息补进来，等它结束再下结论。
		if !r.waitHistoryBackfillIdle(ctx) {
			return
		}
		// 又断线了就交给下一次重连重新探测。
		if !channelEffectivelyOnline(r.channelStatus()) {
			return
		}
		checkCtx, cancel := context.WithTimeout(ctx, seqGapStoreTimeout)
		gap, err := gapStore.GroupSeqGap(checkCtx, query)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("diana inbound seq gap check failed: group=%s: %v", event.GroupID, err)
			}
			return
		}
		if !gap.Known || gap.Missing <= 0 {
			if detected.Missing > 0 {
				r.recordSeqGapLifecycle(ctx, event, probe.seq, detected, attempt, "backfill_gap_resolved",
					fmt.Sprintf("群 %s 断线期间缺的消息已补齐", event.GroupID), nil)
			}
			return
		}
		if detected.Missing == 0 {
			detected = gap
			r.recordSeqGapLifecycle(ctx, event, probe.seq, gap, attempt, "backfill_gap_detected",
				fmt.Sprintf("群 %s 缺少 %d 条消息（seq %d → %d），开始针对性回补", event.GroupID, gap.Missing, gap.PreviousSeq, probe.seq), nil)
		}
		if attempt >= len(seqGapRetryDelays) {
			r.recordSeqGapLifecycle(ctx, event, probe.seq, gap, attempt, "backfill_gap_unresolved",
				fmt.Sprintf("群 %s 多次回补后仍缺 %d 条消息；撤回和系统提示也会占用 seq，确有遗漏可手动回补", event.GroupID, gap.Missing),
				fmt.Errorf("seq %d → %d 之间仍缺 %d 条消息", gap.PreviousSeq, probe.seq, gap.Missing))
			return
		}
		session := HistorySession{
			Kind:          EventKindGroup,
			ID:            event.GroupID,
			Platform:      PlatformOneBotV11,
			ProfileID:     event.ProfileID,
			LastEventTime: gap.PreviousTime,
		}
		if events, fetchErr := r.fetchHistorySerialized(ctx, session); fetchErr != nil {
			if ctx.Err() == nil {
				log.Printf("diana inbound seq gap backfill failed: group=%s: %v", event.GroupID, fetchErr)
			}
		} else if _, enqueueErrs := r.enqueueBackfilledEvents(ctx, store, events); len(enqueueErrs) > 0 {
			log.Printf("diana inbound seq gap backfill incomplete: group=%s: %v", event.GroupID, enqueueErrs)
		}
		timer := time.NewTimer(seqGapRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *Runtime) waitHistoryBackfillIdle(ctx context.Context) bool {
	for r.historyBackfillBusy.Load() {
		timer := time.NewTimer(inboundPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return ctx.Err() == nil
}

func (r *Runtime) recordSeqGapLifecycle(ctx context.Context, event MessageEvent, seq int64, gap GroupSeqGap, attempts int, action, message string, eventErr error) {
	r.recordOneBotConnectionLifecycleWithMetadata(ctx, r.channelStatus(), action, message, eventErr, map[string]any{
		"group_id":      event.GroupID,
		"seq":           seq,
		"previous_seq":  gap.PreviousSeq,
		"missing":       gap.Missing,
		"self_messages": gap.SelfMessages,
		"attempts":      attempts,
	})
}
