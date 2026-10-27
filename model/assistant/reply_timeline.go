// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"
	"time"
)

// 回复时间线：一条消息从进来到第一条回复发出，各阶段落在哪个时刻。
//
// 单次模型调用的耗时轨迹里早就有，但「这条回复为什么 20 秒才出去」要把它们串起来
// 才看得出：路由完到 Agent 开始之间的空档、主回复等首字和吐字各占多少、审核压在
// 发送前多久，这些都不是哪一次调用自己的耗时。时间线挂在调试轨迹上，和它同开同关，
// 整轮结束写一条 reply_timeline 记录。

const (
	replyStageDecided         = "reply_decided"
	replyStageTurnStarted     = "turn_started"
	replyStageCrossGroupReady = "cross_group_ready"
	replyStagePluginsDone     = "plugins_done"
	replyStageToolsReady      = "tools_ready"
	replyStageAgentStarted    = "agent_started"
	replyStageFirstRequest    = "reply_request_started"
	replyStageFirstToken      = "reply_first_token"
	replyStageAgentDone       = "agent_done"
	replyStageAuditStarted    = "audit_started"
	replyStageAuditDone       = "audit_done"
	replyStageFirstSent       = "first_sent"
)

type replyTimeline struct {
	mu       sync.Mutex
	received time.Time
	marks    map[string]time.Time
	written  bool
}

func newReplyTimeline(received time.Time) *replyTimeline {
	return &replyTimeline{received: received, marks: map[string]time.Time{}}
}

func replyTimelineFromContext(ctx context.Context) *replyTimeline {
	state := debugTraceFromContext(ctx)
	for state != nil && state.parent != nil {
		state = state.parent
	}
	if state == nil {
		return nil
	}
	return state.timeline
}

// markReplyStage 只记每个阶段第一次到达的时刻：多步 Agent 会请求很多次模型，
// 要看的是「第一次」离收到消息有多远。
func markReplyStage(ctx context.Context, stage string, at time.Time) {
	timeline := replyTimelineFromContext(ctx)
	if timeline == nil {
		return
	}
	timeline.mu.Lock()
	defer timeline.mu.Unlock()
	if _, seen := timeline.marks[stage]; !seen && !timeline.written {
		timeline.marks[stage] = at
	}
}

// offsets 把各阶段换成相对收到消息的毫秒数，同一轮只交出一次。
func (t *replyTimeline) offsets() (map[string]any, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.written || len(t.marks) == 0 {
		return nil, false
	}
	t.written = true
	out := make(map[string]any, len(t.marks))
	for stage, at := range t.marks {
		out[stage+"_ms"] = at.Sub(t.received).Milliseconds()
	}
	return out, true
}

// recordReplyTimeline 写一条 reply_timeline 轨迹。没走到任何阶段（还没轮到回复就
// 结束了）时不写。
func (r *Runtime) recordReplyTimeline(ctx context.Context, outcome string) {
	state := debugTraceFromContext(ctx)
	for state != nil && state.parent != nil {
		state = state.parent
	}
	if state == nil {
		return
	}
	stages, ok := state.timeline.offsets()
	if !ok {
		return
	}
	metadata := map[string]any{
		"phase":   "reply_timeline",
		"stages":  stages,
		"outcome": outcome,
	}
	// 平台时间戳只到秒，差值只说明「进来前已经在路上多久」，不参与阶段计算。
	if state.event.Time > 0 {
		metadata["platform_lag_ms"] = state.timeline.received.Sub(time.Unix(state.event.Time, 0)).Milliseconds()
	}
	r.recordDebugTrace(state, "回复时间线", metadata)
}
