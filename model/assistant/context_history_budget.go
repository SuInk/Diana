package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const (
	recentHistoryTokenShare       int64 = 55
	longTermMemoryTokenShare      int64 = 10
	compressedSummaryTokenShare   int64 = 15
	coreMemoryTokenShare          int64 = 5
	contextShareDenominator       int64 = 100
	minimumHistoryCandidateTokens int64 = 16
	minimumRecentHistoryTokens    int64 = 512
	maximumHistoryCandidates            = 4096
	recentHistoryPriorityTurns          = 3
)

// 纯百分比预算在大窗口下会失控：55% × 128K 等于每条群消息都注入 70K 历史。历史的
// 价值曲线衰减极快，最近几百条几乎就是全部价值，再往前是白付 prefill 的钱和延迟，
// 还会稀释注意力。所以每层在份额之外再加一个绝对上限，取两者较小值。
//
// 这些是上限不是下限：小窗口下份额本来就更小，仍按份额走，不会因为常量比窗口大
// 而超发。
const (
	// DefaultRecentHistoryTokenBudget 是近期聊天历史的默认绝对上限，约合普通群聊
	// 300–600 条。它按群聊场景估算，没有实测支撑，所以做成可配置而不是常量：
	// 长期只用到一半说明虚高，长期顶满说明该调大。
	DefaultRecentHistoryTokenBudget int64 = 16000
	// sessionThreadTokenCeiling 限制会话线程便签。它天然只有几百字，撞上限通常
	// 说明模型把已完结话题囤在了 thread 里，而不是预算不够。
	sessionThreadTokenCeiling int64 = 1200
	// retrievedMemoryTokenCeiling 限制按相关性检索出来的长期记忆。配合 MMR 的 24
	// 条上限，平均每条约 100 token。
	retrievedMemoryTokenCeiling int64 = 2400
	// coreMemoryTokenCeiling 限制常驻注入的核心记忆（长期交互要求和高置信要害
	// 事实）。它不参加相关性排序，所以配额必须小而固定。
	coreMemoryTokenCeiling int64 = 600
)

// contextLayerBudget 取「份额」和「绝对上限」中较小的那个。
func contextLayerBudget(contextWindow, share, ceiling int64) int64 {
	budget := contextShareBudget(contextWindow, share)
	if ceiling > 0 && budget > ceiling {
		return ceiling
	}
	return budget
}

// recentHistoryBudget 返回本轮近期历史的 token 预算。配置值只能收紧不能放宽：
// 填得比窗口份额还大时仍按份额走，否则单群配置能绕过窗口保护。
func recentHistoryBudget(contextWindow int64, cfg BotConfig) int64 {
	ceiling := cfg.RecentHistoryTokenBudget
	if ceiling <= 0 {
		ceiling = DefaultRecentHistoryTokenBudget
	}
	return contextLayerBudget(contextWindow, recentHistoryTokenShare, ceiling)
}

// sessionThreadBudget 返回会话线程便签的 token 预算。
func sessionThreadBudget(contextWindow int64) int64 {
	return contextLayerBudget(contextWindow, compressedSummaryTokenShare, sessionThreadTokenCeiling)
}

// retrievedMemoryBudget 返回按相关性检索的长期记忆预算。
func retrievedMemoryBudget(contextWindow int64) int64 {
	return contextLayerBudget(contextWindow, longTermMemoryTokenShare, retrievedMemoryTokenCeiling)
}

// coreMemoryBudget 返回常驻核心记忆预算。它同时受 5% 份额约束，避免 8K 本地模型上
// 两个固定项合起来把历史挤到墙角。
func coreMemoryBudget(contextWindow int64) int64 {
	return contextLayerBudget(contextWindow, coreMemoryTokenShare, coreMemoryTokenCeiling)
}

type historyContextTurn struct {
	events       []MessageEvent
	estimated    int64
	lastUserID   string
	lastTime     int64
	hasAssistant bool
}

// promptContextHistory loads a token-derived candidate window, then keeps
// complete recent turns from newest to oldest. RecentContextLimit remains
// available to legacy history tools, but no longer defines the normal prompt.
func (r *Runtime) promptContextHistory(event MessageEvent, cfg BotConfig) []MessageEvent {
	budget := recentHistoryBudget(r.promptContextWindowTokens(event, cfg), cfg)
	// 就着眼前这张图问一句时收紧历史：答案几乎全在图里，长历史是白付的 prefill。
	budget = visionFocusedHistoryBudget(budget, event, PlainText(event.Segments))
	candidateLimit := historyCandidateLimitForBudget(budget)
	session := sessionKey(event)

	r.mu.RLock()
	memory := append([]MessageEvent(nil), r.history[session]...)
	store := r.messageStore
	summaryWatermark := r.contextSummaryWatermarkLocked(session)
	r.mu.RUnlock()

	stored := []MessageEvent(nil)
	if store != nil {
		loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var err error
		stored, err = store.ListRecentMessageEvents(loadCtx, session, candidateLimit)
		cancel()
		if err != nil {
			log.Printf("chatbot token-budget history load failed: %v", err)
		}
	}
	history := dropSummarizedHistory(mergeMessageHistory(memory, stored, candidateLimit), memory, summaryWatermark)
	if strings.TrimSpace(event.MessageID) != "" {
		filtered := history[:0]
		for _, item := range history {
			if item.MessageID != event.MessageID {
				filtered = append(filtered, item)
			}
		}
		history = filtered
	}
	return selectRecentHistoryTurns(history, event, cfg.BotAccount, budget)
}

// promptContextWindowTokens 取本轮可能被选中的配置档里最小的那个请求上下文上限：
// 编排必须对每个候选模型都成立。以前它以 DefaultContextWindowTokens 起算、且只
// 往下收，于是无论模型多大窗口都出不了 16K——那个常量是「没有配置档时的兜底」，
// 不该当成所有模型的天花板。
func (r *Runtime) promptContextWindowTokens(event MessageEvent, cfg BotConfig) int64 {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		// 没有配置档时用兜底常量当窗口，但仍要过下面那道配置收紧：这里以前直接
		// 返回常量，于是机器人或群配的 max_context_tokens 在这条路径上完全不生效。
		return clampContextWindowToConfig(llm.DefaultContextWindowTokens, cfg)
	}
	group := llm.GroupChat
	if hasImageSegment(event.Segments) || (event.Quoted != nil && hasImageSegment(event.Quoted.Segments)) {
		group = llm.GroupVision
	}
	set := store.Profiles().WithDefaults()
	// 与真正发请求时的挑选顺序保持一致：角色绑定 → 分组 → 当前激活配置。
	// 只看角色绑定的话，没配模型角色的部署会一路回落到兜底常量，等于从来没看过
	// 实际在用的模型窗口。
	profiles, _ := r.roleBoundProfiles(set, group)
	if len(profiles) == 0 {
		profiles = llmProfilesInGroup(set, group)
	}
	if len(profiles) == 0 {
		if current, ok := set.Current(); ok {
			profiles = []llm.Profile{current}
		}
	}
	window := int64(0)
	for _, profile := range profiles {
		providerWindow := profile.Config.MaxContextTokensWithDefault()
		if providerWindow <= 0 {
			continue
		}
		if window == 0 || providerWindow < window {
			window = providerWindow
		}
	}
	if window <= 0 {
		window = llm.DefaultContextWindowTokens
	}
	return clampContextWindowToConfig(window, cfg)
}

// clampContextWindowToConfig 让机器人/群配置的上限只能收紧：模型窗口是能力上限，
// 配置里那个数是这个机器人愿意在单次请求上花多少。
func clampContextWindowToConfig(window int64, cfg BotConfig) int64 {
	if cfg.MaxContextTokens > 0 && cfg.MaxContextTokens < window {
		return cfg.MaxContextTokens
	}
	return window
}

func contextShareBudget(contextWindow, share int64) int64 {
	if contextWindow <= 0 {
		contextWindow = llm.DefaultContextWindowTokens
	}
	budget := contextWindow * share / contextShareDenominator
	if share == recentHistoryTokenShare && budget < minimumRecentHistoryTokens {
		return minimumRecentHistoryTokens
	}
	return budget
}

func historyCandidateLimitForBudget(budget int64) int {
	limit := int(budget/minimumHistoryCandidateTokens) + 8
	if limit < 1 {
		limit = 1
	}
	if limit > maximumHistoryCandidates {
		limit = maximumHistoryCandidates
	}
	return limit
}

func selectRecentHistoryTurns(history []MessageEvent, current MessageEvent, botAccount string, budget int64) []MessageEvent {
	if len(history) == 0 || budget <= 0 {
		return nil
	}
	turns := groupHistoryContextTurns(history, current.Time, botAccount)
	remaining := budget
	first := len(turns)
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		if turn.estimated > remaining {
			// Always pass the newest turn to the final request budgeter. It can
			// compact an oversized turn, while dropping it here makes the model
			// appear to forget the message that was just sent.
			if first == len(turns) && index == len(turns)-1 {
				first = index
			}
			break
		}
		remaining -= turn.estimated
		first = index
	}
	if first == len(turns) {
		return nil
	}
	selected := make([]MessageEvent, 0)
	for _, turn := range turns[first:] {
		selected = append(selected, turn.events...)
	}
	return selected
}

func groupHistoryContextTurns(history []MessageEvent, currentTime int64, botAccount string) []historyContextTurn {
	turns := make([]historyContextTurn, 0, len(history))
	for _, event := range history {
		assistantEvent := strings.TrimSpace(event.botReply) != "" || assistantHistoryEvent(event, botAccount)
		cost := estimateHistoryContextEventTokens(event, currentTime, assistantEvent)
		if assistantEvent && len(turns) > 0 && !turns[len(turns)-1].hasAssistant {
			last := &turns[len(turns)-1]
			last.events = append(last.events, event)
			last.estimated += cost
			last.hasAssistant = true
			last.lastTime = max(last.lastTime, event.Time)
			continue
		}
		if !assistantEvent && len(turns) > 0 {
			last := &turns[len(turns)-1]
			closeInTime := event.Time > 0 && last.lastTime > 0 && event.Time-last.lastTime >= 0 && event.Time-last.lastTime <= 120
			if !last.hasAssistant && closeInTime && strings.TrimSpace(last.lastUserID) == strings.TrimSpace(event.UserID) {
				last.events = append(last.events, event)
				last.estimated += cost
				last.lastTime = event.Time
				continue
			}
		}
		turns = append(turns, historyContextTurn{
			events:       []MessageEvent{event},
			estimated:    cost,
			lastUserID:   event.UserID,
			lastTime:     event.Time,
			hasAssistant: assistantEvent,
		})
	}
	return turns
}

func historyContextMetadata(history []MessageEvent, currentTime int64, botAccount string) (map[string]string, map[string]bool) {
	groups := make(map[string]string, len(history))
	recent := make(map[string]bool, len(history))
	turns := groupHistoryContextTurns(history, currentTime, botAccount)
	recentFrom := max(0, len(turns)-recentHistoryPriorityTurns)
	for turnIndex, turn := range turns {
		groupID := "history-turn-" + fmt.Sprint(turnIndex)
		for _, event := range turn.events {
			if key := messageHistoryDedupeKey(event); key != "" {
				groups[key] = groupID
				recent[key] = turnIndex >= recentFrom
			}
		}
	}
	return groups, recent
}

func estimateHistoryContextEventTokens(event MessageEvent, currentTime int64, assistantEvent bool) int64 {
	text := ""
	if strings.TrimSpace(event.botReply) != "" {
		text = event.botReply
	} else if assistantEvent {
		text = historyPlainText(event)
	} else {
		text = historyPromptTextAt(event, currentTime)
	}
	// The final prompt adds message framing, and image summaries can be filled in
	// after selection. Reserve enough space without charging full vision tokens,
	// because historical images are represented by text until explicitly loaded.
	cost := llm.EstimateTextTokens(text) + 8
	cost += int64(historicalStillImageCount(event)) * 192
	if cost < minimumHistoryCandidateTokens {
		cost = minimumHistoryCandidateTokens
	}
	return cost
}

func (r *Runtime) recordPromptContextBudget(ctx context.Context, event MessageEvent, cfg BotConfig, messages []llm.Message, history []MessageEvent, semantic semanticReferencePromptContext, sources semanticReferenceContext, summaryRecompressed bool) {
	if !cfg.DebugModeEnabled {
		return
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	categoryTokens := map[string]int64{
		"system": 0, "plugin_and_tools": 0, "memory": 0,
		"summary": 0, "history": 0, "current": 0,
	}
	for _, message := range messages {
		cost := llm.EstimateMessageTokens(message)
		switch message.Priority {
		case llm.MessagePriorityCurrent:
			categoryTokens["current"] += cost
		case llm.MessagePriorityPlugin:
			categoryTokens["plugin_and_tools"] += cost
		case llm.MessagePriorityMemory:
			categoryTokens["memory"] += cost
		case llm.MessagePrioritySummary:
			categoryTokens["summary"] += cost
		case llm.MessagePrioritySystem:
			categoryTokens["system"] += cost
		default:
			categoryTokens["history"] += cost
		}
	}
	window := r.promptContextWindowTokens(event, cfg)
	earliest, latest := int64(0), int64(0)
	for _, item := range history {
		if item.Time <= 0 {
			continue
		}
		if earliest == 0 || item.Time < earliest {
			earliest = item.Time
		}
		if item.Time > latest {
			latest = item.Time
		}
	}
	breakdown := llm.PlanContextBudget(messages, window, llm.DefaultMaxOutputTokens)
	metadata := map[string]any{
		"effective_context_window":  breakdown.ContextWindow,
		"output_reserve":            breakdown.OutputReserve,
		"safety_reserve":            breakdown.SafetyReserve,
		"input_budget":              breakdown.InputBudget,
		"requested_tokens":          breakdown.RequestedTokens,
		"selected_tokens":           breakdown.SelectedTokens,
		"dropped_tokens":            breakdown.DroppedTokens,
		"over_budget":               breakdown.OverBudget,
		"categories":                contextBudgetCategoryTrace(breakdown),
		"category_tokens":           categoryTokens,
		"history_token_budget":      recentHistoryBudget(window, cfg),
		"history_token_share":       contextShareBudget(window, recentHistoryTokenShare),
		"summary_token_budget":      sessionThreadBudget(window),
		"memory_token_budget":       retrievedMemoryBudget(window) + coreMemoryBudget(window),
		"history_selected_messages": len(history),
		"history_selected_turns":    len(groupHistoryContextTurns(history, event.Time, cfg.BotAccount)),
		"history_earliest_time":     earliest,
		"history_latest_time":       latest,
		"summary":                   contextBudgetSummaryTrace(breakdown, sessionThreadBudget(window), summaryRecompressed),
		"semantic_requested":        semantic.Requested,
		"semantic_resolved":         semantic.Resolved,
		"semantic_text_sources":     semantic.TextSources,
		"semantic_expected_images":  semantic.ExpectedImages,
		"semantic_attached_images":  sources.AttachedImageCount,
		"semantic_missing_sources":  sources.MissingSourceCount,
		"message_id":                event.MessageID,
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:     applog.KindDebug,
		Level:    applog.LevelInfo,
		Action:   "chatbot.context_budget",
		Message:  "上下文预算已编排",
		Actor:    "chat_context",
		Target:   strings.TrimSpace(event.MessageID),
		Metadata: metadata,
	})
}

// contextBudgetReasonText keeps the debug trace readable in the WebUI without
// forcing the log reader to memorize the machine-readable slugs.
func contextBudgetReasonText(reason string) string {
	switch reason {
	case llm.ContextBudgetReasonFits:
		return "完整保留"
	case llm.ContextBudgetReasonTrimmed:
		return "超出可用额度，内容已按预算裁剪"
	case llm.ContextBudgetReasonOldestTurnCut:
		return "输入预算不足，最旧的完整轮次未装入"
	case llm.ContextBudgetReasonBudgetExceeded:
		return "输入预算已用尽，本类内容被丢弃"
	default:
		return reason
	}
}

func contextBudgetCategoryTrace(breakdown llm.ContextBudgetBreakdown) []map[string]any {
	categories := make([]map[string]any, 0, len(breakdown.Categories))
	for _, category := range breakdown.Categories {
		categories = append(categories, map[string]any{
			"category":           category.Category,
			"priority":           category.Priority,
			"requested_messages": category.RequestedMessages,
			"selected_messages":  category.SelectedMessages,
			"dropped_messages":   category.DroppedMessages,
			"trimmed_messages":   category.TrimmedMessages,
			"requested_tokens":   category.RequestedTokens,
			"selected_tokens":    category.SelectedTokens,
			"dropped_tokens":     category.DroppedTokens,
			"reason":             category.Reason,
			"reason_text":        contextBudgetReasonText(category.Reason),
		})
	}
	return categories
}

// contextBudgetSummaryTrace reports the compressed-summary block separately,
// because a silently shortened summary loses entity relations and time bounds
// in a way a plain token count does not make obvious.
func contextBudgetSummaryTrace(breakdown llm.ContextBudgetBreakdown, target int64, recompressed bool) map[string]any {
	trace := map[string]any{
		"present":          false,
		"target_tokens":    target,
		"requested_tokens": int64(0),
		"selected_tokens":  int64(0),
		"recompressed":     recompressed,
		"dropped":          false,
	}
	for _, category := range breakdown.Categories {
		if category.Category != llm.ContextBudgetCategoryName(llm.MessagePrioritySummary) {
			continue
		}
		trace["present"] = category.RequestedMessages > 0
		trace["requested_tokens"] = category.RequestedTokens
		trace["selected_tokens"] = category.SelectedTokens
		// 注入前已经重新压缩过，或请求预算层又裁了一刀，都算摘要被缩短。
		trace["recompressed"] = recompressed || category.TrimmedMessages > 0
		trace["dropped"] = category.DroppedMessages > 0
		trace["reason"] = category.Reason
		trace["reason_text"] = contextBudgetReasonText(category.Reason)
	}
	return trace
}

// dropSummarizedHistory 去掉已经被折进压缩摘要、却仍留在存储层的原文。
// 摘要与原文同时进入一个请求，既浪费预算，也让模型看到同一段对话的两个版本。
// 仍在内存历史里的事件一律保留：内存历史就是「还没被压缩」的定义，即使它与最后
// 一条被压缩的消息同秒，也属于原始窗口。
func dropSummarizedHistory(history, memory []MessageEvent, watermark int64) []MessageEvent {
	if watermark <= 0 || len(history) == 0 {
		return history
	}
	retained := make(map[string]bool, len(memory))
	for _, event := range memory {
		if key := messageHistoryDedupeKey(event); key != "" {
			retained[key] = true
		}
	}
	filtered := make([]MessageEvent, 0, len(history))
	for _, event := range history {
		// 时间未知的事件无法与水位比较，保留它比丢掉它安全。
		if event.Time > 0 && event.Time <= watermark && !retained[messageHistoryDedupeKey(event)] {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

// ContextBudgetLayer 是上下文预算里的一层，用于对外展示。
type ContextBudgetLayer struct {
	// Key 供前端上色和排序，不随文案改动。
	Key string `json:"key"`
	// Label 是这一层的中文名。
	Label string `json:"label"`
	// SharePercent 是这一层的窗口份额，Ceiling 是绝对上限。
	SharePercent int64 `json:"share_percent"`
	Ceiling      int64 `json:"ceiling"`
	// Tokens 是两者取小之后本群实际生效的预算。
	Tokens int64 `json:"tokens"`
	// CappedByCeiling 说明这一层是被绝对上限压住的，还是窗口份额本来就更小。
	// 大窗口下几乎全是前者，小窗口下全是后者——这正是分层预算要表达的事。
	CappedByCeiling bool `json:"capped_by_ceiling"`
	// Configurable 标记这一层能不能在配置里调。只有近期历史可以：另外三层的
	// 取值由各自的结构决定（便签天然只有几百字、检索层配合 MMR 的条数上限、
	// 常驻层不参加相关性排序所以必须小而固定），给旋钮也没有对应的失效模式。
	Configurable bool `json:"configurable"`
}

// ContextBudgetBreakdown 是某个会话当前的上下文预算分配。
type ContextBudgetBreakdown struct {
	GroupID string `json:"group_id,omitempty"`
	// ContextWindow 是这个群实际会用到的模型窗口。
	ContextWindow int64                `json:"context_window"`
	Layers        []ContextBudgetLayer `json:"layers"`
	// Allocated 是四层合计，Headroom 是留给系统提示、当前消息、工具结果和输出的余量。
	Allocated int64 `json:"allocated"`
	Headroom  int64 `json:"headroom"`
}

// ContextBudgetBreakdownForGroup 按群算出当前的预算分配。
//
// 它复用运行时真正用的那几个预算函数，不另算一遍：分配图一旦自己实现一份
// min(份额, 上限)，改了预算逻辑而忘了改图，图就开始骗人。
func (r *Runtime) ContextBudgetBreakdownForGroup(groupID string) ContextBudgetBreakdown {
	event := MessageEvent{Kind: EventKindGroup, GroupID: strings.TrimSpace(groupID)}
	if event.GroupID == "" {
		event.Kind = EventKindPrivate
	}
	cfg := r.effectiveConfigForEvent(event)
	window := r.promptContextWindowTokens(event, cfg)

	historyCeiling := cfg.RecentHistoryTokenBudget
	if historyCeiling <= 0 {
		historyCeiling = DefaultRecentHistoryTokenBudget
	}
	breakdown := ContextBudgetBreakdown{
		GroupID:       event.GroupID,
		ContextWindow: window,
		Layers: []ContextBudgetLayer{
			newContextBudgetLayer("recent_history", "近期历史", window, recentHistoryTokenShare, historyCeiling, true),
			newContextBudgetLayer("session_thread", "会话便签", window, compressedSummaryTokenShare, sessionThreadTokenCeiling, false),
			newContextBudgetLayer("retrieved_memory", "检索记忆", window, longTermMemoryTokenShare, retrievedMemoryTokenCeiling, false),
			newContextBudgetLayer("core_memory", "常驻记忆", window, coreMemoryTokenShare, coreMemoryTokenCeiling, false),
		},
	}
	for _, layer := range breakdown.Layers {
		breakdown.Allocated += layer.Tokens
	}
	if breakdown.Headroom = window - breakdown.Allocated; breakdown.Headroom < 0 {
		breakdown.Headroom = 0
	}
	return breakdown
}

func newContextBudgetLayer(key, label string, window, share, ceiling int64, configurable bool) ContextBudgetLayer {
	tokens := contextLayerBudget(window, share, ceiling)
	return ContextBudgetLayer{
		Key:             key,
		Label:           label,
		SharePercent:    share,
		Ceiling:         ceiling,
		Tokens:          tokens,
		CappedByCeiling: ceiling > 0 && contextShareBudget(window, share) > ceiling,
		Configurable:    configurable,
	}
}
