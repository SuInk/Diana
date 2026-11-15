// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// 上下文以滑动窗口为主、摘要为辅，切点和摘要写法照 Pi（earendil-works/pi
// coding-agent 的 compaction）：
//
//   - 平时不裁剪、不丢弃：会话从起点（锚）往后的历史全部原样带上，只往尾部追加。
//   - 滑窗随时做、不调模型：回复后请求接近阈值、发之前估算超过「窗口 −
//     compactionReserveTokens」，或供应商报上下文超限时，从最新往回累计到 compactionKeepRecent，在一轮发言的开头切开，
//     锚挪到第一条保留的消息（见 slideContextWindow）。
//   - 摘要只在会话空闲时写（见 idle_compaction.go）：滑走的那段交给模型增量并进摘要
//     （见 summarizeSlidContext），输出上限照 Pi 取 0.8 × 预留。
//   - 模型看得到发生了什么：摘要以「此前的对话已压缩为以下摘要」开头放在历史最前，
//     写明更早的原文要用 chat_history 查；还没补进摘要的那段也写明。
//
// 和 Pi 不同的地方：
//   - 会话起点：群聊没有「会话开始」，冷启动（没有锚）时从最新往回取保留量作为起点，
//     并在历史开头写明更早的消息没有载入。
//   - 时机：Pi 在超限时同步写摘要；这里滑窗和写摘要分开，有人等回复时只滑窗。
//   - 保留量：不照 Pi 只留 2 万，保留「阈值减去非历史部分」的一半，原文占大头。
//   - 小窗口：Pi 的 16384 是按大窗口定的。窗口小时预留按 1/4 缩，阈值不超过发送层的
//     输入预算，否则滑完照样超限。
const (
	// compactionReserveTokens 对应 Pi 的 reserveTokens（默认 16384）。
	compactionReserveTokens int64 = 16384
	// compactionReserveDivisor 让小窗口的预留按比例缩：窗口的 1/4，最多 16384（64K 及
	// 以上的窗口就是 Pi 的原值）。
	compactionReserveDivisor int64 = 4
	// sideContextKeepRecentTokens 是旁路（主动开口、入群欢迎、挑新闻检索词）带的近期
	// 原文上限：它们不是接着聊，不跟主回复一起放到半个窗口。
	sideContextKeepRecentTokens int64 = 20000
	// minimumHistoryCandidateTokens 是一条历史的最小估算体积，用来换算要从存储里读
	// 多少条才一定盖住锚之后的全部历史。
	minimumHistoryCandidateTokens int64 = 16
	// historyContextGroupPrefix 标出一条消息属于哪条历史，压缩时按它把被摘要的原文
	// 从已组装的请求里摘掉。
	historyContextGroupPrefix = "history:"
	// compactionHeaderContextGroup 标出历史开头那条说明（摘要或冷启动提示）。
	compactionHeaderContextGroup = "history-header"
)

// compactionWindow 是算压缩阈值要用的两个数：模型窗口和为输出留的量。
type compactionWindow struct {
	Tokens    int64
	MaxOutput int64
}

func (w compactionWindow) tokens() int64 {
	if w.Tokens <= 0 {
		return llm.DefaultContextWindowTokens
	}
	return w.Tokens
}

// compactionReserve 是 Pi 的 reserveTokens：大窗口 16384，小窗口按 1/4 缩。
func compactionReserve(window compactionWindow) int64 {
	return min(compactionReserveTokens, window.tokens()/compactionReserveDivisor)
}

// compactionThreshold 是触发压缩的请求估算 token 数（Pi 的 contextWindow − reserveTokens），
// 不超过发送层为输入留的预算（窗口减输出上限）：超过它的请求本地校验就会拒。
func compactionThreshold(window compactionWindow) int64 {
	tokens := window.tokens()
	return max(1, min(tokens-compactionReserve(window), llm.InputTokenBudget(tokens, window.MaxOutput)))
}

// compactionSummaryMaxTokens 是写摘要时的输出上限，照 Pi 的
// min(0.8 × reserveTokens, 模型输出上限)：大窗口约 1.3 万，小窗口跟着预留缩。摘要只是
// 辅助，压完的请求里原文占大头。
func compactionSummaryMaxTokens(window compactionWindow) int64 {
	limit := compactionReserve(window) * 8 / 10
	if window.MaxOutput > 0 {
		limit = min(limit, window.MaxOutput)
	}
	return max(1, limit)
}

// compactionKeepRecent 是压缩时原样保留的近期历史：「阈值减去非历史部分（系统提示、
// 记忆、当前消息、工具定义）」的一半。不照 Pi 只留 2 万：聊天里最重要的是原文，压完
// 原文仍占请求的大头，另一半留给之后的追加。小到 0 时 compactionCut 仍会保留最新的一轮。
func compactionKeepRecent(window compactionWindow, nonHistory int64) int64 {
	return max(0, (compactionThreshold(window)-nonHistory)/2)
}

// historyCandidateLimit 返回要从存储读多少条历史：按最小体积换算，读满阈值那么多条，
// 锚之后的历史一般都在里面。锚不在里面时 promptContextHistory 会再往前读。
func historyCandidateLimit(window compactionWindow) int {
	return int(compactionThreshold(window)/minimumHistoryCandidateTokens) + 8
}

// promptCompactionWindow 取对话模型的窗口和输出上限。输出上限取候选配置档里最大的
// 那个（没填按 DefaultMaxOutputTokens），保证每个候选模型都装得下。
func (r *Runtime) promptCompactionWindow(event MessageEvent, cfg BotConfig) compactionWindow {
	window := compactionWindow{Tokens: r.promptContextWindowTokens(event, cfg)}
	for _, profile := range r.promptChatProfiles() {
		if limit := profile.Config.MaxOutputTokens; limit > window.MaxOutput {
			window.MaxOutput = limit
		}
	}
	return window
}

const compactionSummaryPrefix = "【此前的对话历史已压缩为以下摘要；摘要之后是保留下来的原文】\n<summary>\n"
const compactionSummarySuffix = "\n</summary>"
const compactionUnloadedNote = "更早的聊天原文没有放进上下文，需要时用 chat_history 查。"
const compactionPendingNote = "摘要之后、下面的原文之前还有一段聊天已经滑出窗口、还没写进摘要，需要时用 chat_history 查。"

// compactionHeaderText 渲染历史开头的说明。有摘要时是摘要本身（滑窗后还没补进摘要
// 的那段另行写明）；没有摘要时写明历史从哪一刻开始载入。都只在滑窗、补摘要或重新
// 锚定时变化，跟着前缀缓存走。
func compactionHeaderText(state compactionCheckpoint, firstLoaded MessageEvent) string {
	if summary := strings.TrimSpace(state.Summary); summary != "" {
		text := compactionSummaryPrefix + summary + compactionSummarySuffix + "\n" + compactionUnloadedNote
		if state.PendingFrom != "" {
			text += compactionPendingNote
		}
		return text
	}
	if firstLoaded.Time <= 0 {
		return ""
	}
	start := contextSummaryTimeLabel(firstLoaded.Time, profileLocation(firstLoaded.ProfileID))
	return fmt.Sprintf("【以下聊天记录从 %s 起】%s", start, compactionUnloadedNote)
}

func compactionHeaderMessage(state compactionCheckpoint, history []MessageEvent) (llm.Message, bool) {
	var first MessageEvent
	for _, item := range history {
		if !item.crossGroupContext {
			first = item
			break
		}
	}
	text := compactionHeaderText(state, first)
	if text == "" {
		return llm.Message{}, false
	}
	return llm.Message{
		Role:         llm.RoleUser,
		Content:      text,
		Priority:     llm.MessagePrioritySummary,
		AtomicText:   true,
		ContextGroup: compactionHeaderContextGroup,
	}, true
}

// compactionAnchorKey 是锚的存放键：群按机器人和平台区分，私聊按会话。
func compactionAnchorKey(event MessageEvent) string {
	if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" {
		return groupPromptSessionKey(event)
	}
	return sessionKey(event)
}

// compactionCheckpoint 是一次压缩留下的状态：摘要正文和它覆盖到的最后一条消息时间。
type compactionCheckpoint struct {
	Summary string
	Until   int64
	// UntilKey 是被摘要的最后一条消息的去重键，按它接回，不靠精确到秒的时间。
	UntilKey string
	// PendingFrom 是滑出窗口、还没写进摘要的第一条消息的去重键：滑窗随时做，摘要等
	// 会话空闲才补（见 idle_compaction.go）。空表示摘要和原文之间没有空档。
	PendingFrom string
}

// compactionState 返回这个会话当前的压缩摘要。群的摘要存在 GroupPromptSession 里，
// 重启后还在；私聊只在内存里，重启后丢失，从冷启动重新开始。
func (r *Runtime) compactionState(event MessageEvent) compactionCheckpoint {
	if s := r.groupPromptSession(event); s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.invalidated {
			return compactionCheckpoint{}
		}
		return compactionCheckpoint{Summary: s.data.Checkpoint, Until: s.data.CheckpointUntil, UntilKey: s.data.CheckpointUntilKey, PendingFrom: s.data.CheckpointPendingFrom}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.contextSummaries[sessionKey(event)]
}

func (r *Runtime) compactionSummary(event MessageEvent) string {
	return r.compactionState(event).Summary
}

// storeCompaction 一起记下新的摘要和锚：两者必须同时生效，否则模型会看到摘要和
// 被摘要的原文两份，或者两边都没有。
func (r *Runtime) storeCompaction(event MessageEvent, state compactionCheckpoint, anchor string) {
	r.setHistoryWindowAnchor(compactionAnchorKey(event), anchor)
	if s := r.groupPromptSession(event); s != nil {
		s.mu.Lock()
		if !s.invalidated {
			s.data.Checkpoint = state.Summary
			s.data.CheckpointUntil = state.Until
			s.data.CheckpointUntilKey = state.UntilKey
			s.data.CheckpointPendingFrom = state.PendingFrom
			s.data.Anchor = anchor
			s.saveLocked()
		}
		s.mu.Unlock()
		return
	}
	r.mu.Lock()
	if state.Summary == "" && state.PendingFrom == "" {
		delete(r.contextSummaries, sessionKey(event))
	} else {
		r.contextSummaries[sessionKey(event)] = state
	}
	r.mu.Unlock()
}

// historyCoversCheckpoint 报告已读到的历史够不够用：锚在里面，或者最早一条不晚于
// 摘要覆盖到的时间（摘要之后的消息一条没漏），或者是冷启动（既没有锚也没有摘要）。
func historyCoversCheckpoint(history []MessageEvent, anchor string, state compactionCheckpoint) bool {
	if historyWindowAnchorIndex(history, anchor) >= 0 || historyWindowAnchorIndex(history, state.UntilKey) >= 0 {
		return true
	}
	if state.Summary == "" || state.Until <= 0 {
		// 没有摘要要接时，锚也没有就是冷启动，读到的就够了。
		return anchor == ""
	}
	return len(history) > 0 && history[0].Time > 0 && history[0].Time <= state.Until
}

// sessionHistoryWindow 返回从锚开始的全部历史。
//
//   - 锚在历史里：从锚开始全部带上。
//   - 锚丢了（被撤回、读取范围外）但有摘要：从摘要覆盖到的时间之后第一条开始，
//     摘要和原文之间不留空档；这段太长时由压缩并进摘要。
//   - 摘要之后的消息已经不可得（存储里没有了）：摘要作废、清掉，按冷启动处理，
//     历史开头写明从哪一刻开始载入。
//   - 冷启动：从最新往回取保留量作为起点。
func (r *Runtime) sessionHistoryWindow(event MessageEvent, history []MessageEvent, botAccount string, window compactionWindow) []MessageEvent {
	key := compactionAnchorKey(event)
	promptSession := r.groupPromptSession(event)
	if anchor := promptSession.anchor(); anchor != "" {
		r.setHistoryWindowAnchor(key, anchor)
	}
	if len(history) == 0 {
		return nil
	}
	anchor := r.historyWindowAnchor(key)
	if index := historyWindowAnchorIndex(history, anchor); index >= 0 {
		return history[index:]
	}
	state := r.compactionState(event)
	if state.Summary != "" {
		resume := -1
		if index := historyWindowAnchorIndex(history, state.UntilKey); index >= 0 {
			// 被摘要的最后一条还在：从它下一条接上，同一秒的消息一条不丢。
			resume = index + 1
		} else if len(history) > 0 && history[0].Time > 0 && history[0].Time <= state.Until {
			// 那一条本身不在了（撤回）：按时间接，同一秒的宁可多带一条也不漏。
			resume = len(history)
			for index, item := range history {
				if item.Time >= state.Until {
					resume = index
					break
				}
			}
		}
		if resume >= 0 {
			if resume >= len(history) {
				// 摘要之后还没有新消息。
				return nil
			}
			next := messageHistoryDedupeKey(history[resume])
			r.setHistoryWindowAnchor(key, next)
			promptSession.rememberAnchor(next)
			return history[resume:]
		}
		log.Printf("diana context compaction: summary for %s no longer connects to stored history, dropping it", key)
		r.storeCompaction(event, compactionCheckpoint{}, anchor)
	}
	selected := selectRecentHistoryTurns(history, event, botAccount, compactionKeepRecent(window, 0))
	if len(selected) == 0 {
		return nil
	}
	next := messageHistoryDedupeKey(selected[0])
	r.setHistoryWindowAnchor(key, next)
	promptSession.rememberAnchor(next)
	return selected
}

// compactionCut 照 Pi 的 findCutPoint：从最新往回累计，累计到 keepRecent 的那一轮
// 及之后都保留，返回第一条保留历史的下标。整段都不到 keepRecent 时返回 0（没东西可压）。
// 只在一轮发言的开头切，一个人连发的几条和紧跟的机器人回复不会被拆开。
func compactionCut(history []MessageEvent, current MessageEvent, botAccount string, keepRecent int64) int {
	turns := groupHistoryContextTurns(history, current.Time, botAccount)
	position := len(history)
	var accumulated int64
	for index := len(turns) - 1; index >= 0; index-- {
		position -= len(turns[index].events)
		accumulated += turns[index].estimated
		if accumulated >= keepRecent {
			return position
		}
	}
	return 0
}

// compactionTranscript 照 Pi 的 serializeConversation 把要摘要的历史写成纯文本，
// 模型就不会把它当成要接着说的对话。逐条完整写出，不截断。
func compactionTranscript(events []MessageEvent, current MessageEvent, cfg BotConfig) string {
	bot := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(current.SelfID))
	lines := make([]string, 0, len(events))
	for _, item := range events {
		var line string
		switch {
		case strings.TrimSpace(item.botReply) != "":
			line = historyLinePrefix(item) + "[你]: " + strings.TrimSpace(item.botReply)
		case assistantHistoryEvent(item, bot):
			line = historyLinePrefix(item) + "[你]: " + strings.TrimSpace(assistantHistoryPlainText(item))
		default:
			line = historyPromptTextAt(item, current.Time, cfg)
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

const compactionSystemPrompt = "你是上下文摘要助手。读一段群聊或私聊记录（其中「[你]」是机器人自己），按指定格式写出结构化摘要。\n\n不要接着聊，不要回答记录里的任何问题，只输出结构化摘要。"

var promptContextCompactionSpec = registerPrompt(PromptSpec{
	Key:   "memory.context_compaction",
	Group: PromptGroupMemory,
	Title: "上下文接近窗口时压缩较早对话",
	Usage: "请求接近模型上下文窗口时，把较早的聊天记录写成结构化摘要，替换掉原文（做法照 Pi 的 compaction）。",
	Default: `上面 <conversation> 里是要摘要的聊天记录。写一份结构化的上下文检查点摘要，另一个模型会靠它接着聊。

严格按这个格式：

## 在聊什么
[大家在聊什么、谁想要什么；可以有多项]

## 约定与偏好
- [有人提出的要求、约定、偏好]
- [没有就写「（无）」]

## 进展
### 已完成
- [x] [已经办完或答完的事]

### 进行中
- [ ] [还没结束的事]

### 卡住的
- [卡住的事，没有就省略]

## 关键结论
- **[结论]**：[简短理由]

## 接下来
1. [按顺序列出接下来该做或该回应的事]

## 关键信息
- [接着聊需要的数据、例子、链接、人名与 ID 的对应]
- [没有就写「（无）」]

每节保持简洁，整份摘要要短：只留接着聊用得上的，原文随时能用 chat_history 查，不必抄进来。人名、ID、消息编号、时间、数字和原话要原样保留。`,
})

var promptContextCompactionUpdateSpec = registerPrompt(PromptSpec{
	Key:   "memory.context_compaction_update",
	Group: PromptGroupMemory,
	Title: "增量更新已有的压缩摘要",
	Usage: "已有压缩摘要时，把新一段较早的聊天记录并进去（做法照 Pi 的 compaction）。",
	Default: `上面 <conversation> 里是新的聊天记录，要并进 <previous-summary> 里的已有摘要。规则：
- 保留已有摘要的全部信息
- 加进新记录里的进展、结论和信息
- 更新「进展」：做完的从「进行中」挪到「已完成」
- 按现状更新「接下来」
- 人名、ID、消息编号、时间、数字和原话要原样保留
- 已经不再相关的删掉，整份摘要要短：原文随时能用 chat_history 查，不必抄进来

严格按这个格式：

## 在聊什么
[保留已有的，话题扩展了就补上]

## 约定与偏好
- [保留已有的，补上新的]

## 进展
### 已完成
- [x] [已有的加新完成的]

### 进行中
- [ ] [按进展更新]

### 卡住的
- [解决了就删掉]

## 关键结论
- **[结论]**：[简短理由]（保留已有的，补上新的）

## 接下来
1. [按现状更新]

## 关键信息
- [保留重要的，补上新的]

每节保持简洁。`,
})

// errNothingToCompact 表示历史全部落在保留区里，压缩腾不出地方。
var errNothingToCompact = errors.New("context compaction: nothing older than the kept recent history")

// compactionLock 返回这个会话的压缩锁：同一个群并发的几次回复只滑一次窗，补摘要时
// 读写检查点也拿它。用容量为 1 的通道当锁，等锁的一方能随 ctx 放弃。调模型写摘要时
// 不拿它，回复路径的滑窗不会被摘要模型拖住。
func (r *Runtime) compactionLock(key string) chan struct{} {
	lock, _ := r.compactionLocks.LoadOrStore(key, make(chan struct{}, 1))
	return lock.(chan struct{})
}

// summaryLock 让同一个会话同时只有一次补摘要。
func (r *Runtime) summaryLock(key string) chan struct{} {
	lock, _ := r.summaryLocks.LoadOrStore(key, make(chan struct{}, 1))
	return lock.(chan struct{})
}

func acquire(ctx context.Context, lock chan struct{}) error {
	select {
	case lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// slideContextWindow 把最旧的原文整段滑出窗口：从最新往回保留 keepRecent（见
// compactionKeepRecent），锚挪到第一条保留的消息，不调模型。滑走的部分记进
// PendingFrom，等会话空闲时再补进摘要（见 summarizeSlidContext）。reason 写进日志，
// 取 "threshold"、"overflow"（同 Pi）或 "after_reply"（见 idle_compaction.go）。
//
// 返回保留下来的历史和滑窗后的检查点。等锁期间别的回复已经滑过（锚挪到了这份历史
// 中间），就直接用它的结果。
func (r *Runtime) slideContextWindow(ctx context.Context, event MessageEvent, cfg BotConfig, history []MessageEvent, keepRecent int64, reason string) ([]MessageEvent, compactionCheckpoint, error) {
	var local []MessageEvent
	for _, item := range history {
		if !item.crossGroupContext {
			local = append(local, item)
		}
	}
	if len(local) == 0 {
		return nil, compactionCheckpoint{}, errNothingToCompact
	}
	key := compactionAnchorKey(event)
	lock := r.compactionLock(key)
	if err := acquire(ctx, lock); err != nil {
		return nil, compactionCheckpoint{}, err
	}
	defer func() { <-lock }()
	if index := historyWindowAnchorIndex(local, r.historyWindowAnchor(key)); index > 0 {
		return local[index:], r.compactionState(event), nil
	}
	cut := compactionCut(local, event, cfg.BotAccount, keepRecent)
	if cut <= 0 || cut >= len(local) {
		return nil, compactionCheckpoint{}, errNothingToCompact
	}
	slid, kept := local[:cut], local[cut:]
	state := r.compactionState(event)
	if state.PendingFrom == "" {
		state.PendingFrom = messageHistoryDedupeKey(slid[0])
	}
	r.storeCompaction(event, state, messageHistoryDedupeKey(kept[0]))
	// 滑窗后的上下文和上一轮发出的请求不再同前缀，续接记录作废，下一步整份重新组装。
	r.appendContexts.put(appendContextKey(event), nil)
	log.Printf("diana context slide: reason=%s message_id=%s slid=%d kept=%d", reason, event.MessageID, len(slid), len(kept))
	r.recordContextCompaction(ctx, event, reason, len(slid), len(kept))
	return kept, state, nil
}

// summarizeSlidContext 把滑出窗口、还没写进摘要的那段增量并进摘要（照 Pi 的
// generateSummary，摘要有输出上限）。只在会话空闲时调用。调模型期间又滑过窗的话，
// 这次只补到开始时的锚，新滑走的那段留给下一次。
func (r *Runtime) summarizeSlidContext(ctx context.Context, event MessageEvent, cfg BotConfig) error {
	key := compactionAnchorKey(event)
	summaryLock := r.summaryLock(key)
	if err := acquire(ctx, summaryLock); err != nil {
		return err
	}
	defer func() { <-summaryLock }()

	lock := r.compactionLock(key)
	if err := acquire(ctx, lock); err != nil {
		return err
	}
	previous := r.compactionState(event)
	anchor := r.historyWindowAnchor(key)
	<-lock
	if previous.PendingFrom == "" {
		return nil
	}
	window := r.promptCompactionWindow(event, cfg)
	history := r.loadSessionHistory(event, window, func(history []MessageEvent) bool {
		return historyWindowAnchorIndex(history, previous.PendingFrom) >= 0
	})
	start, end := historyWindowAnchorIndex(history, previous.PendingFrom), historyWindowAnchorIndex(history, anchor)
	if start < 0 || end <= start {
		// 那段已经不可得（撤回、清空），或者锚丢了以后从摘要处重新接上、那段又回到了
		// 窗口里：没有要补的，清掉标记，开头不再写空档。
		return r.commitSlidSummary(event, previous, compactionCheckpoint{Summary: previous.Summary, Until: previous.Until, UntilKey: previous.UntilKey}, anchor)
	}
	var pending []MessageEvent
	for _, item := range history[start:end] {
		if !item.crossGroupContext {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return r.commitSlidSummary(event, previous, compactionCheckpoint{Summary: previous.Summary, Until: previous.Until, UntilKey: previous.UntilKey}, anchor)
	}
	header, previousBody := splitContextSummary(previous.Summary)
	since, count := parseContextSummaryHeader(header)
	if since == "" {
		since = contextSummaryTimeLabel(pending[0].Time, profileLocation(pending[0].ProfileID))
	}
	last := pending[len(pending)-1]
	until := contextSummaryTimeLabel(last.Time, profileLocation(last.ProfileID))

	body := strings.TrimSpace(strings.Join(previousBody, "\n"))
	// 待摘要的部分可能比摘要模型的窗口还大（机器人很久没说话、群里一直在聊）：按块
	// 切开，逐块增量更新摘要，每块都带着上一块的结果。
	threshold := compactionThreshold(window)
	maxSummary := compactionSummaryMaxTokens(window)
	// 提示词、标签和消息框架的开销，取两种提示词里长的那个，再留一点余量。
	overhead := llm.EstimateTextTokens(compactionSystemPrompt) + max(llm.EstimateTextTokens(cfg.prompt(promptContextCompactionSpec)), llm.EstimateTextTokens(cfg.prompt(promptContextCompactionUpdateSpec))) + 512
	// 每块的预算按当前摘要的长度重新算：摘要逐块变长，后面的块要给它让地方。
	for remaining := pending; len(remaining) > 0; {
		chunk, rest := nextCompactionChunk(remaining, event, cfg, threshold-overhead-llm.EstimateTextTokens(body))
		next, err := r.summarizeCompactionChunk(ctx, cfg, chunk, body, maxSummary)
		if err != nil {
			return err
		}
		body, remaining = next, rest
	}
	next := compactionCheckpoint{
		Summary:  joinContextSummary(contextSummaryHeader(since, until, count+len(pending)), []string{body}),
		Until:    last.Time,
		UntilKey: messageHistoryDedupeKey(last),
	}
	if err := r.commitSlidSummary(event, previous, next, anchor); err != nil {
		return err
	}
	log.Printf("diana context summary: message_id=%s summarized=%d", event.MessageID, len(pending))
	r.recordContextCompaction(ctx, event, "summary", len(pending), 0)
	return nil
}

// commitSlidSummary 存下补好的摘要。调模型期间检查点被换掉（清空了上下文、别处改过）
// 就作废；锚又往后滑过的话，从开始时的锚起仍欠着。
func (r *Runtime) commitSlidSummary(event MessageEvent, previous, next compactionCheckpoint, anchor string) error {
	key := compactionAnchorKey(event)
	lock := r.compactionLock(key)
	lock <- struct{}{}
	defer func() { <-lock }()
	current := r.compactionState(event)
	if current != previous {
		return errSummaryStale
	}
	currentAnchor := r.historyWindowAnchor(key)
	if currentAnchor != anchor {
		next.PendingFrom = anchor
	}
	r.storeCompaction(event, next, currentAnchor)
	// 历史开头的摘要变了，续接记录作废，下一轮整份重新组装。
	r.appendContexts.put(appendContextKey(event), nil)
	return nil
}

// errSummaryStale 表示补摘要期间检查点被换掉了，这次的结果作废。
var errSummaryStale = errors.New("context compaction: checkpoint changed while summarizing")

// nextCompactionChunk 从待摘要的历史开头切出一块写成文字，估算体积不超过 budget；
// 单条超过 budget 的独占一块，不截断。返回这一块和剩下的历史。
func nextCompactionChunk(events []MessageEvent, current MessageEvent, cfg BotConfig, budget int64) (string, []MessageEvent) {
	var size int64
	end := 0
	for end < len(events) {
		cost := llm.EstimateTextTokens(compactionTranscript(events[end:end+1], current, cfg))
		if end > 0 && size+cost > budget {
			break
		}
		size += cost
		end++
	}
	return compactionTranscript(events[:end], current, cfg), events[end:]
}

// summarizeCompactionChunk 照 Pi 的 generateSummary：对话序列化成纯文本放进
// <conversation>，有旧摘要就放进 <previous-summary> 用增量更新提示词。摘要由对话
// 模型自己写（同 Pi 用会话模型），用途记为 context_summary_compaction。
//
// maxTokens 是摘要的输出上限（见 compactionSummaryMaxTokens）。
func (r *Runtime) summarizeCompactionChunk(ctx context.Context, cfg BotConfig, transcript, previous string, maxTokens int64) (string, error) {
	prompt := "<conversation>\n" + transcript + "\n</conversation>\n\n"
	spec := promptContextCompactionSpec
	if previous != "" {
		prompt += "<previous-summary>\n" + previous + "\n</previous-summary>\n\n"
		spec = promptContextCompactionUpdateSpec
	}
	prompt += cfg.prompt(spec)
	summaryCtx := withLLMUsagePurpose(ctx, PurposeContextSummary)
	text, err := r.runLLMProviderForGroup(summaryCtx, llm.GroupChat, func(provider LLMProvider) (string, error) {
		response, err := provider.Generate(summaryCtx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactionSystemPrompt},
			{Role: llm.RoleUser, Content: prompt, Priority: llm.MessagePriorityCurrent, AtomicText: true},
		}, MaxOutputTokens: maxTokens})
		if err != nil {
			return "", err
		}
		if response == nil || len(response.ToolCalls) > 0 {
			return "", fmt.Errorf("context compaction: summary response is empty or calls a tool")
		}
		return response.Text, nil
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("context compaction: empty summary")
	}
	return strings.TrimSpace(text), nil
}

// applyCompactionToMessages 把已组装的请求改成压缩后的样子：被摘要的历史原文摘掉，
// 历史开头的说明换成新摘要，其它消息原样不动。第二个返回值是每条旧消息的新下标，
// 被摘掉的是 -1。
func applyCompactionToMessages(messages []llm.Message, kept []MessageEvent, state compactionCheckpoint) ([]llm.Message, []int) {
	keep := make(map[string]bool, len(kept))
	for _, item := range kept {
		keep[historyContextGroupPrefix+messageHistoryDedupeKey(item)] = true
	}
	header, _ := compactionHeaderMessage(state, kept)
	out := make([]llm.Message, 0, len(messages)+1)
	positions := make([]int, len(messages))
	headerIndex := -1
	insertHeader := func() {
		if headerIndex < 0 {
			headerIndex = len(out)
			out = append(out, header)
		}
	}
	// 同一天的历史行只写时分秒（见 history_line_dates.go）：摘掉开头几行后，留下的
	// 第一行可能正好没写日期。先按原件推出每行的全日期，再按留下的这份重新决定省不省。
	var expand, compact historyLineDates
	for index, message := range messages {
		positions[index] = -1
		if message.Role == llm.RoleUser {
			message.Content = expand.expand(message.Content)
		}
		switch {
		case message.ContextGroup == compactionHeaderContextGroup:
			insertHeader()
			positions[index] = headerIndex
			continue
		case strings.HasPrefix(message.ContextGroup, historyContextGroupPrefix):
			if !keep[message.ContextGroup] {
				// 缓存断点落在被摘掉的消息上时挪到摘要上，稳定前缀的标记不能跟着消失。
				insertHeader()
				if message.CacheBreakpoint {
					out[headerIndex].CacheBreakpoint = true
				}
				continue
			}
			insertHeader()
		}
		if message.Role == llm.RoleUser {
			message.Content = compact.compact(message.Content)
		}
		positions[index] = len(out)
		out = append(out, message)
	}
	return out, positions
}

// estimateReplyRequestTokens 估算这一轮主回复第一步请求的体积：消息加工具定义。
func estimateReplyRequestTokens(messages []llm.Message, toolTokens int64) int64 {
	return estimateMessagesTokens(messages) + toolTokens
}

// replyFixedOverheadTokens 估算 Runner 在消息之外每步都带的体积：Agent 协议与按需
// 目录、Skill 目录、工具定义。Runner 建好之后才知道这一轮挂了哪些，所以取这个会话
// 最近一轮记下的常驻开销（ResidentFootprint）；还没跑过时按注册表里全部工具的定义
// 估，宁可偏大。
func (r *Runtime) replyFixedOverheadTokens(event MessageEvent, registry *agent.ToolRegistry) int64 {
	groupID := ""
	if event.Kind == EventKindGroup {
		groupID = event.GroupID
	}
	r.agentResidencyMu.RLock()
	footprint, ok := r.agentFootprints[agentFootprintKey(event.ProfileID, groupID)]
	if !ok {
		footprint, ok = r.agentFootprints[strings.TrimSpace(event.ProfileID)+agentFootprintAnyGroup]
	}
	r.agentResidencyMu.RUnlock()
	if ok {
		return footprint.toolTokens + llm.EstimateTextTokens(footprint.systemPrompt) + llm.EstimateTextTokens(footprint.skillsCatalog)
	}
	if registry != nil {
		return llm.EstimateToolDefinitionsTokens(registry.Definitions())
	}
	return 0
}

// compactReplyMessages 滑一次窗并把已组装的主回复请求改成滑窗后的样子，不调模型。track 指向
// 请求里某条消息的下标（例如依赖图那条），摘掉前面的历史后跟着前移。压缩失败时
// 原样返回 false，这一轮照原请求发，超限就如实报错。
func (r *Runtime) compactReplyMessages(ctx context.Context, event MessageEvent, cfg BotConfig, registry *agent.ToolRegistry, history []MessageEvent, messages []llm.Message, reason string, track *int) ([]llm.Message, []MessageEvent, bool) {
	_, nonHistory := replyRequestTokens(messages, r.replyFixedOverheadTokens(event, registry))
	keepRecent := compactionKeepRecent(r.promptCompactionWindow(event, cfg), nonHistory)
	kept, state, err := r.slideContextWindow(ctx, event, cfg, history, keepRecent, reason)
	if err != nil {
		if !errors.Is(err, errNothingToCompact) {
			log.Printf("diana context slide failed: reason=%s message_id=%s err=%v", reason, event.MessageID, err)
		}
		return messages, history, false
	}
	// 滑走的那段等会话空闲时再补进摘要。
	r.scheduleIdleCompaction(event)
	next, positions := applyCompactionToMessages(messages, kept, state)
	if track != nil && *track >= 0 && *track < len(positions) {
		*track = positions[*track]
	}
	// 跨群参考不在压缩范围内，原样跟在保留的历史后面。
	for _, item := range history {
		if item.crossGroupContext {
			kept = append(kept, item)
		}
	}
	return next, kept, true
}

// recordContextCompaction 把压缩写进运行日志：界面上要答得了「为什么回复像是只记得
// 摘要」，以及压缩有多频繁。
func (r *Runtime) recordContextCompaction(ctx context.Context, event MessageEvent, reason string, summarized, kept int) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	message := "上下文超过压缩阈值，最旧的原文已滑出窗口"
	switch reason {
	case "overflow":
		message = "模型报上下文超限，最旧的原文已滑出窗口后重试"
	case "after_reply":
		message = "上下文接近压缩阈值，回复后把最旧的原文滑出了窗口"
	case "summary":
		message = "会话空闲时把滑出窗口的原文补进了摘要"
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "context_compaction",
		Message: message,
		Actor:   "chat_context",
		Target:  strings.TrimSpace(event.MessageID),
		Metadata: map[string]any{
			"reason":     reason,
			"session":    sessionKey(event),
			"summarized": summarized,
			"kept":       kept,
		},
	})
}

// sideContextHistory 给主动开口、入群欢迎、挑新闻检索词这些旁路用：带压缩摘要和最近
// 的保留区（同压缩时保留的那么多），不把整个窗口的历史发出去。保留区之前、摘要之后
// 还有消息时，在开头那条说明里写明没带上多少条、要用 chat_history 查。
func (r *Runtime) sideContextHistory(event MessageEvent, cfg BotConfig) (llm.Message, []MessageEvent) {
	history := sessionOnlyHistory(r.promptContextHistory(event, cfg))
	recent := selectRecentHistoryTurns(history, event, cfg.BotAccount, min(sideContextKeepRecentTokens, compactionKeepRecent(r.promptCompactionWindow(event, cfg), 0)))
	omitted := len(history) - len(recent)
	state := r.compactionState(event)
	summary := strings.TrimSpace(state.Summary)
	var text string
	switch {
	case summary != "" && omitted > 0 && state.PendingFrom == "":
		text = compactionSummaryPrefix + summary + compactionSummarySuffix + fmt.Sprintf("\n摘要之后、下面的记录之前还有 %d 条消息没有带上，需要时用 chat_history 查。", omitted)
	case summary != "":
		text = compactionHeaderText(state, MessageEvent{})
	case len(recent) > 0:
		text = compactionHeaderText(compactionCheckpoint{}, recent[0])
		if omitted > 0 {
			text += fmt.Sprintf("（这之前还有 %d 条本会话消息没有带上）", omitted)
		}
	}
	header := llm.Message{Role: llm.RoleUser, Content: text, Priority: llm.MessagePrioritySummary, AtomicText: true, ContextGroup: compactionHeaderContextGroup}
	return header, recent
}
