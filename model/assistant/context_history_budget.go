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
	contextShareDenominator       int64 = 100
	minimumHistoryCandidateTokens int64 = 16
	minimumRecentHistoryTokens    int64 = 512
	maximumHistoryCandidates            = 4096
	recentHistoryPriorityTurns          = 3
)

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
	budget := contextShareBudget(r.promptContextWindowTokens(event, cfg), recentHistoryTokenShare)
	candidateLimit := historyCandidateLimitForBudget(budget)
	session := sessionKey(event)

	r.mu.RLock()
	memory := append([]MessageEvent(nil), r.history[session]...)
	store := r.messageStore
	r.mu.RUnlock()

	stored := []MessageEvent(nil)
	if store != nil {
		loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var err error
		stored, err = store.ListRecentMessageEvents(loadCtx, session, candidateLimit)
		cancel()
		if err != nil {
			log.Printf("qqbot token-budget history load failed: %v", err)
		}
	}
	history := mergeMessageHistory(memory, stored, candidateLimit)
	if strings.TrimSpace(event.MessageID) != "" {
		filtered := history[:0]
		for _, item := range history {
			if item.MessageID != event.MessageID {
				filtered = append(filtered, item)
			}
		}
		history = filtered
	}
	return selectRecentHistoryTurns(history, event, cfg.BotQQ, budget)
}

func (r *Runtime) promptContextWindowTokens(event MessageEvent, cfg BotConfig) int64 {
	window := llm.DefaultContextWindowTokens
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return window
	}
	group := llm.GroupChat
	if hasImageSegment(event.Segments) || (event.Quoted != nil && hasImageSegment(event.Quoted.Segments)) {
		group = llm.GroupVision
	}
	profiles, _ := r.roleBoundProfiles(store.Profiles().WithDefaults(), group)
	for _, profile := range profiles {
		providerWindow := profile.Config.MaxContextTokensWithDefault()
		if providerWindow > 0 && providerWindow < window {
			window = providerWindow
		}
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

func selectRecentHistoryTurns(history []MessageEvent, current MessageEvent, botQQ string, budget int64) []MessageEvent {
	if len(history) == 0 || budget <= 0 {
		return nil
	}
	turns := groupHistoryContextTurns(history, current.Time, botQQ)
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

func groupHistoryContextTurns(history []MessageEvent, currentTime int64, botQQ string) []historyContextTurn {
	turns := make([]historyContextTurn, 0, len(history))
	for _, event := range history {
		assistantEvent := strings.TrimSpace(event.botReply) != "" || assistantHistoryEvent(event, botQQ)
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

func historyContextMetadata(history []MessageEvent, currentTime int64, botQQ string) (map[string]string, map[string]bool) {
	groups := make(map[string]string, len(history))
	recent := make(map[string]bool, len(history))
	turns := groupHistoryContextTurns(history, currentTime, botQQ)
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
		"history_token_budget":      contextShareBudget(window, recentHistoryTokenShare),
		"summary_token_budget":      contextShareBudget(window, compressedSummaryTokenShare),
		"memory_token_budget":       contextShareBudget(window, longTermMemoryTokenShare),
		"history_selected_messages": len(history),
		"history_selected_turns":    len(groupHistoryContextTurns(history, event.Time, cfg.BotQQ)),
		"history_earliest_time":     earliest,
		"history_latest_time":       latest,
		"summary":                   contextBudgetSummaryTrace(breakdown, contextShareBudget(window, compressedSummaryTokenShare), summaryRecompressed),
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
		Action:   "qqbot.context_budget",
		Message:  "上下文预算已编排",
		Actor:    "qq_context",
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
