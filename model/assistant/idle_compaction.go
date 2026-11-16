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

// 上下文的主机制是滑动窗口，摘要只是辅助。
//
//   - 滑窗不调模型，随时可以做：把最旧的原文整段滑出窗口（挪锚），原文回到「阈值减去
//     非历史部分」的一半。一轮回复的请求超过阈值的 idleCompactionTriggerPercent，
//     生成完就马上滑；已经超过阈值的，发之前当场滑。
//   - 写摘要要调模型，只在会话空闲时做：滑走、还没写进摘要的那段增量并进一段很短的
//     摘要（输出上限见 compactionSummaryMaxTokens）。补上之前，历史开头写明摘要和原文
//     之间有一段要用 chat_history 查。
//
// 空闲指会话安静满 idleCompactionDelay：每来一条消息（包括机器人自己发的）都往后推，
// 有回复在跑也不算空闲。

// idleCompactionDelay 是会话安静多久才算空闲。测试里会改小。
var idleCompactionDelay = 2 * time.Minute

// idleCompactionTimeout 是一次后台补摘要最长跑多久（可能要分几块调模型）。
const idleCompactionTimeout = 5 * time.Minute

// idleCompactionTriggerPercent：请求超过压缩阈值的这个百分比，生成完就滑窗。
const idleCompactionTriggerPercent int64 = 85

// slideAfterReplyTimeout 是回复后滑窗（只读历史、挪锚）最长跑多久。
const slideAfterReplyTimeout = 30 * time.Second

// idleCompactionState 是一个会话等着空闲时补的那次摘要。
type idleCompactionState struct {
	timer *time.Timer
	// event 是挂上定时器的那一轮要回复的消息，补摘要照它找会话和配置。
	event MessageEvent
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

// noteReplyRequestSize 在一轮回复的请求组装好、没超阈值时调用，返回生成完要不要马上
// 滑窗。不用滑但还欠着摘要的，等空闲时补上。
func (r *Runtime) noteReplyRequestSize(event MessageEvent, window compactionWindow, total int64) bool {
	if total*100 >= compactionThreshold(window)*idleCompactionTriggerPercent {
		return true
	}
	if r.compactionState(event).PendingFrom != "" {
		r.scheduleIdleCompaction(event)
	}
	return false
}

// slideAfterReply 在回复生成完后到后台滑一次窗，再等空闲补摘要。不占着回复：滑窗要
// 读历史，发送不用等它。
func (r *Runtime) slideAfterReply(event MessageEvent, cfg BotConfig, nonHistory int64) {
	go func() {
		defer recoverGoroutinePanic("context_slide_after_reply")
		ctx, cancel := context.WithTimeout(r.backgroundContext(), slideAfterReplyTimeout)
		defer cancel()
		window := r.promptCompactionWindow(event, cfg)
		history := r.promptContextHistory(event, cfg)
		if _, _, err := r.slideContextWindow(ctx, event, cfg, history, compactionKeepRecent(window, nonHistory), "after_reply"); err != nil {
			if !errors.Is(err, errNothingToCompact) {
				log.Printf("diana context slide failed: reason=after_reply message_id=%s err=%v", event.MessageID, err)
			}
			return
		}
		r.scheduleIdleCompaction(event)
	}()
}

// scheduleIdleCompaction 挂上（或刷新）这个会话空闲时补摘要的定时器。
func (r *Runtime) scheduleIdleCompaction(event MessageEvent) {
	key := compactionAnchorKey(event)
	r.idleCompactions.mu.Lock()
	defer r.idleCompactions.mu.Unlock()
	if r.idleCompactions.pending == nil {
		r.idleCompactions.pending = map[string]*idleCompactionState{}
	}
	if state := r.idleCompactions.pending[key]; state != nil {
		state.event = event
		state.timer.Reset(idleCompactionDelay)
		return
	}
	state := &idleCompactionState{event: event}
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

// backgroundContext 是后台滑窗、补摘要用的 context：运行中跟着运行时停，没在运行
// （测试）时用 Background。
func (r *Runtime) backgroundContext() context.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.runCtx != nil {
		return r.runCtx
	}
	return context.Background()
}

func (r *Runtime) runIdleCompaction(key string, state *idleCompactionState) {
	r.idleCompactions.mu.Lock()
	if r.idleCompactions.pending[key] != state {
		r.idleCompactions.mu.Unlock()
		return
	}
	event := state.event
	if r.sessionReplyRunning(sessionKey(event)) {
		// 有回复在跑就不算空闲：等它发完（发出的消息还会再推一次）。
		state.timer.Reset(idleCompactionDelay)
		r.idleCompactions.mu.Unlock()
		return
	}
	delete(r.idleCompactions.pending, key)
	r.idleCompactions.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.backgroundContext(), idleCompactionTimeout)
	defer cancel()
	if err := r.summarizeSlidContext(ctx, event, r.effectiveConfigForEvent(event)); err != nil {
		log.Printf("diana context summary failed: message_id=%s err=%v", event.MessageID, err)
	}
}
