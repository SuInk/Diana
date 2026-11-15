package assistant

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 空闲压缩：上下文的主机制是滑动窗口，摘要只是辅助。
//
// 回复路径上的阈值压缩（见 context_compaction.go）要等请求真的超过阈值才做，而且是
// 在有人等回复的时候同步调一次模型。这里把它挪到会话空闲时：一轮回复的请求超过阈值
// 的 idleCompactionTriggerPercent，就给这个会话挂一个定时器；之后会话里每来一条消息
// （包括机器人自己发的）都把它往后推。安静满 idleCompactionDelay、也没有回复在跑时，
// 在后台把最旧的原文整段滑出窗口，原文回到「阈值减去非历史部分」的一半，滑走的部分
// 并进一段很短的摘要（输出上限见 compactionSummaryMaxTokens），历史开头写明更早的
// 原文要用 chat_history 查。
//
// 群里一直在刷、等不到空闲时，回复路径的阈值压缩照旧兜底。

// idleCompactionDelay 是会话安静多久才压缩。测试里会改小。
var idleCompactionDelay = 2 * time.Minute

// idleCompactionTimeout 是一次后台压缩最长跑多久（摘要可能要分几块调模型）。
const idleCompactionTimeout = 5 * time.Minute

// idleCompactionTriggerPercent：请求超过压缩阈值的这个百分比，就等空闲时压缩。
const idleCompactionTriggerPercent int64 = 85

// idleCompactionState 是一个会话等着的那次空闲压缩。
type idleCompactionState struct {
	timer *time.Timer
	// event 是挂上定时器的那一轮要回复的消息，压缩照它找会话、配置和历史。
	event MessageEvent
	// nonHistory 是那一轮请求里历史以外的部分（系统提示、记忆、当前消息、工具定义）。
	nonHistory int64
}

type idleCompactions struct {
	mu      sync.Mutex
	pending map[string]*idleCompactionState
}

// replyRequestTokens 返回主回复请求的估算体积和其中历史以外的部分。
func replyRequestTokens(messages []llm.Message, overhead int64) (total, nonHistory int64) {
	var historyTokens int64
	for _, message := range messages {
		if strings.HasPrefix(message.ContextGroup, historyContextGroupPrefix) || message.ContextGroup == compactionHeaderContextGroup {
			historyTokens += llm.EstimateMessageTokens(message)
		}
	}
	total = estimateReplyRequestTokens(messages, overhead)
	return total, total - historyTokens
}

// noteReplyRequestSize 在一轮回复的请求组装好之后调用：超过阈值的
// idleCompactionTriggerPercent 就挂上（或刷新）这个会话的空闲压缩。
func (r *Runtime) noteReplyRequestSize(event MessageEvent, window compactionWindow, total, nonHistory int64) {
	if total*100 < compactionThreshold(window)*idleCompactionTriggerPercent {
		return
	}
	key := compactionAnchorKey(event)
	r.idleCompactions.mu.Lock()
	defer r.idleCompactions.mu.Unlock()
	if r.idleCompactions.pending == nil {
		r.idleCompactions.pending = map[string]*idleCompactionState{}
	}
	if state := r.idleCompactions.pending[key]; state != nil {
		state.event, state.nonHistory = event, nonHistory
		state.timer.Reset(idleCompactionDelay)
		return
	}
	state := &idleCompactionState{event: event, nonHistory: nonHistory}
	state.timer = time.AfterFunc(idleCompactionDelay, func() { r.runIdleCompaction(key, state) })
	r.idleCompactions.pending[key] = state
}

// postponeIdleCompaction 在会话来了新消息时把等着的空闲压缩往后推。
func (r *Runtime) postponeIdleCompaction(event MessageEvent) {
	key := compactionAnchorKey(event)
	r.idleCompactions.mu.Lock()
	defer r.idleCompactions.mu.Unlock()
	if state := r.idleCompactions.pending[key]; state != nil {
		state.timer.Reset(idleCompactionDelay)
	}
}

// stopIdleCompactions 在运行时停止时撤掉所有还没到点的空闲压缩。
func (r *Runtime) stopIdleCompactions() {
	r.idleCompactions.mu.Lock()
	defer r.idleCompactions.mu.Unlock()
	for key, state := range r.idleCompactions.pending {
		state.timer.Stop()
		delete(r.idleCompactions.pending, key)
	}
}

// sessionReplyRunning 判断这个会话有没有还在跑的回复。
func (r *Runtime) sessionReplyRunning(session string) bool {
	r.replyProgressMu.Lock()
	defer r.replyProgressMu.Unlock()
	return len(r.sessionReplyProgresses[session]) > 0
}

func (r *Runtime) runIdleCompaction(key string, state *idleCompactionState) {
	r.idleCompactions.mu.Lock()
	if r.idleCompactions.pending[key] != state {
		r.idleCompactions.mu.Unlock()
		return
	}
	event, nonHistory := state.event, state.nonHistory
	if r.sessionReplyRunning(sessionKey(event)) {
		// 有回复在跑就不算空闲：等它发完（发出的消息还会再推一次）。
		state.timer.Reset(idleCompactionDelay)
		r.idleCompactions.mu.Unlock()
		return
	}
	delete(r.idleCompactions.pending, key)
	r.idleCompactions.mu.Unlock()

	r.mu.Lock()
	base := r.runCtx
	r.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, idleCompactionTimeout)
	defer cancel()
	cfg := r.effectiveConfigForEvent(event)
	window := r.promptCompactionWindow(event, cfg)
	history := r.promptContextHistory(event, cfg)
	if _, _, err := r.compactContext(ctx, event, cfg, history, compactionKeepRecent(window, nonHistory), "idle"); err != nil && !errors.Is(err, errNothingToCompact) {
		log.Printf("diana context compaction failed: reason=idle message_id=%s err=%v", event.MessageID, err)
	}
}
