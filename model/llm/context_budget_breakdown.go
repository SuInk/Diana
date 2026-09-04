// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "sort"

// Drop reasons reported per prompt category. They are stable slugs so callers
// can localize or aggregate them without parsing prose.
const (
	ContextBudgetReasonFits           = "fits_budget"
	ContextBudgetReasonTrimmed        = "trimmed_to_fit"
	ContextBudgetReasonOldestTurnCut  = "oldest_turns_did_not_fit"
	ContextBudgetReasonBudgetExceeded = "input_budget_exhausted"
)

// ContextBudgetCategoryUsage explains what one prompt layer asked for and what
// actually survived the request budgeter.
type ContextBudgetCategoryUsage struct {
	Category          string `json:"category"`
	Priority          int    `json:"priority"`
	RequestedMessages int    `json:"requested_messages"`
	SelectedMessages  int    `json:"selected_messages"`
	DroppedMessages   int    `json:"dropped_messages"`
	TrimmedMessages   int    `json:"trimmed_messages"`
	RequestedTokens   int64  `json:"requested_tokens"`
	SelectedTokens    int64  `json:"selected_tokens"`
	DroppedTokens     int64  `json:"dropped_tokens"`
	Reason            string `json:"reason"`
}

// ContextBudgetBreakdown is a redacted, per-category account of one request's
// token allocation. It carries no message content, only counts.
type ContextBudgetBreakdown struct {
	ContextWindow   int64                        `json:"effective_context_window"`
	OutputReserve   int64                        `json:"output_reserve"`
	SafetyReserve   int64                        `json:"safety_reserve"`
	InputBudget     int64                        `json:"input_budget"`
	RequestedTokens int64                        `json:"requested_tokens"`
	SelectedTokens  int64                        `json:"selected_tokens"`
	DroppedTokens   int64                        `json:"dropped_tokens"`
	OverBudget      bool                         `json:"over_budget"`
	Categories      []ContextBudgetCategoryUsage `json:"categories"`
}

// PlanContextBudget runs the same selection the provider applies before sending,
// then reports what each category requested, kept and lost. It mutates nothing,
// so callers can log the breakdown next to the request they are about to make.
func PlanContextBudget(messages []Message, contextWindow, outputReserve int64) ContextBudgetBreakdown {
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	if outputReserve <= 0 {
		outputReserve = DefaultMaxOutputTokens
	}
	budget := InputTokenBudget(contextWindow, outputReserve)
	breakdown := ContextBudgetBreakdown{
		ContextWindow: contextWindow,
		OutputReserve: outputReserve,
		SafetyReserve: contextBudgetSafetyReserve,
		InputBudget:   budget,
	}
	if len(messages) == 0 {
		return breakdown
	}

	lastIndex := len(messages) - 1
	_, selected := fitMessagesToTokenBudgetDetailed(append([]Message(nil), messages...), budget)
	usage := map[MessagePriority]*ContextBudgetCategoryUsage{}
	priorityOf := func(index int) MessagePriority {
		return effectiveMessagePriority(messages[index], index == lastIndex)
	}
	for index := range messages {
		priority := priorityOf(index)
		entry := usage[priority]
		if entry == nil {
			entry = &ContextBudgetCategoryUsage{
				Category: ContextBudgetCategoryName(priority),
				Priority: int(priority),
				Reason:   ContextBudgetReasonFits,
			}
			usage[priority] = entry
		}
		requested := estimateMessageTokens(messages[index])
		entry.RequestedMessages++
		entry.RequestedTokens += requested
		breakdown.RequestedTokens += requested

		kept, ok := selected[index]
		if !ok {
			entry.DroppedMessages++
			entry.DroppedTokens += requested
			breakdown.DroppedTokens += requested
			continue
		}
		keptTokens := estimateMessageTokens(kept)
		entry.SelectedMessages++
		entry.SelectedTokens += keptTokens
		breakdown.SelectedTokens += keptTokens
		if keptTokens < requested {
			entry.TrimmedMessages++
			entry.DroppedTokens += requested - keptTokens
			breakdown.DroppedTokens += requested - keptTokens
		}
	}
	breakdown.OverBudget = breakdown.RequestedTokens > budget

	breakdown.Categories = make([]ContextBudgetCategoryUsage, 0, len(usage))
	for priority, entry := range usage {
		entry.Reason = contextBudgetDropReason(priority, *entry)
		breakdown.Categories = append(breakdown.Categories, *entry)
	}
	sort.Slice(breakdown.Categories, func(left, right int) bool {
		return breakdown.Categories[left].Priority > breakdown.Categories[right].Priority
	})
	return breakdown
}

// FitMessagesToContextBudget returns the exact prompt messages that survive the
// same budget pass used by providers. It is intended for local diagnostics that
// must distinguish context merely considered from context actually sent.
func FitMessagesToContextBudget(messages []Message, contextWindow, outputReserve int64) []Message {
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	if outputReserve <= 0 {
		outputReserve = DefaultMaxOutputTokens
	}
	return fitMessagesToTokenBudget(append([]Message(nil), messages...), InputTokenBudget(contextWindow, outputReserve))
}

func contextBudgetDropReason(priority MessagePriority, usage ContextBudgetCategoryUsage) string {
	switch {
	case usage.DroppedMessages > 0 && priority <= MessagePriorityHistory:
		// History is filled newest to oldest and stops at the first turn that
		// does not fit, so losses here are always at the old end.
		return ContextBudgetReasonOldestTurnCut
	case usage.DroppedMessages > 0:
		return ContextBudgetReasonBudgetExceeded
	case usage.TrimmedMessages > 0:
		return ContextBudgetReasonTrimmed
	default:
		return ContextBudgetReasonFits
	}
}

// ContextBudgetCategoryName maps a message priority onto the prompt layer names
// used by the budget documentation and the debug trace.
func ContextBudgetCategoryName(priority MessagePriority) string {
	switch priority {
	case MessagePriorityCurrent:
		return "current"
	case MessagePriorityPlugin:
		return "plugin_and_tools"
	case MessagePrioritySystem:
		return "system"
	case MessagePriorityRecentHistory:
		return "recent_history"
	case MessagePriorityMemory:
		return "memory"
	case MessagePrioritySummary:
		return "summary"
	case MessagePriorityHistory:
		return "history"
	default:
		return "other"
	}
}
