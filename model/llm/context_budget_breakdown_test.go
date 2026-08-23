// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"strings"
	"testing"
)

func TestPlanContextBudgetReportsEveryCategoryWithinInputBudget(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: "人设与安全规则", Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: "【较早上下文压缩摘要】" + strings.Repeat("旧事", 400), Priority: MessagePrioritySummary},
		{Role: RoleUser, Content: "【长期记忆】" + strings.Repeat("记忆", 200), Priority: MessagePriorityMemory},
		{Role: RoleUser, Content: strings.Repeat("很久以前的闲聊", 600), Priority: MessagePriorityHistory},
		{Role: RoleUser, Content: strings.Repeat("刚刚的对话", 100), Priority: MessagePriorityRecentHistory},
		{Role: RoleUser, Content: "现在的问题是什么", Priority: MessagePriorityCurrent},
	}
	breakdown := PlanContextBudget(messages, 4096, 512)

	if breakdown.InputBudget != InputTokenBudget(4096, 512) {
		t.Fatalf("input budget = %d, want %d", breakdown.InputBudget, InputTokenBudget(4096, 512))
	}
	if breakdown.ContextWindow != 4096 || breakdown.OutputReserve != 512 {
		t.Fatalf("window/reserve = %d/%d", breakdown.ContextWindow, breakdown.OutputReserve)
	}
	if !breakdown.OverBudget {
		t.Fatalf("expected the oversized prompt to be reported as over budget")
	}
	if breakdown.SelectedTokens > breakdown.InputBudget {
		t.Fatalf("selected %d tokens exceeds input budget %d", breakdown.SelectedTokens, breakdown.InputBudget)
	}

	total := int64(0)
	seen := map[string]ContextBudgetCategoryUsage{}
	for _, category := range breakdown.Categories {
		if category.RequestedTokens != category.SelectedTokens+category.DroppedTokens {
			t.Fatalf("%s: requested %d != selected %d + dropped %d",
				category.Category, category.RequestedTokens, category.SelectedTokens, category.DroppedTokens)
		}
		if category.RequestedMessages != category.SelectedMessages+category.DroppedMessages {
			t.Fatalf("%s: message counts do not balance: %+v", category.Category, category)
		}
		if category.Reason == "" {
			t.Fatalf("%s: missing drop reason", category.Category)
		}
		total += category.SelectedTokens
		seen[category.Category] = category
	}
	if total != breakdown.SelectedTokens {
		t.Fatalf("category selected sum = %d, breakdown = %d", total, breakdown.SelectedTokens)
	}

	current, ok := seen["current"]
	if !ok || current.DroppedMessages != 0 || current.TrimmedMessages != 0 {
		t.Fatalf("current message must survive intact, got %+v", current)
	}
	if _, ok := seen["system"]; !ok {
		t.Fatalf("system category missing from breakdown: %+v", seen)
	}
	history, ok := seen["history"]
	if !ok || history.DroppedTokens == 0 {
		t.Fatalf("expected the oldest history to be dropped, got %+v", history)
	}
	if history.Reason != ContextBudgetReasonOldestTurnCut {
		t.Fatalf("history reason = %q, want %q", history.Reason, ContextBudgetReasonOldestTurnCut)
	}
}

func TestPlanContextBudgetReportsFitsWhenNothingIsDropped(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: "人设", Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: "在吗", Priority: MessagePriorityCurrent},
	}
	breakdown := PlanContextBudget(messages, 0, 0)

	if breakdown.ContextWindow != DefaultContextWindowTokens || breakdown.OutputReserve != DefaultMaxOutputTokens {
		t.Fatalf("defaults not applied: %+v", breakdown)
	}
	if breakdown.OverBudget || breakdown.DroppedTokens != 0 {
		t.Fatalf("small prompt should not drop anything: %+v", breakdown)
	}
	if breakdown.RequestedTokens != breakdown.SelectedTokens {
		t.Fatalf("requested %d != selected %d", breakdown.RequestedTokens, breakdown.SelectedTokens)
	}
	for _, category := range breakdown.Categories {
		if category.Reason != ContextBudgetReasonFits {
			t.Fatalf("%s reason = %q, want %q", category.Category, category.Reason, ContextBudgetReasonFits)
		}
	}
}

func TestAtomicTextMessageIsDroppedWholeInsteadOfTruncated(t *testing.T) {
	summary := "【较早上下文摘要范围：2026-08-18 09:00 ~ 2026-08-19 08:40，共 120 条】\n" + strings.Repeat("周五上线由 Alice 负责，Bob 做回归。", 200)
	messages := []Message{
		{Role: RoleSystem, Content: "人设与安全规则", Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: summary, Priority: MessagePrioritySummary, AtomicText: true},
		{Role: RoleUser, Content: "现在的问题是什么", Priority: MessagePriorityCurrent},
	}
	fitted := fitMessagesToTokenBudget(messages, 512)

	for _, message := range fitted {
		if message.Priority != MessagePrioritySummary {
			continue
		}
		t.Fatalf("atomic summary survived in shortened form: %q", message.Content)
	}
	if len(fitted) == 0 {
		t.Fatal("dropping the summary must not drop the whole request")
	}
	last := fitted[len(fitted)-1]
	if last.Content != "现在的问题是什么" {
		t.Fatalf("current message did not survive: %q", last.Content)
	}
}

func TestAtomicTextMessageSurvivesWholeWhenItFits(t *testing.T) {
	summary := "【较早上下文摘要范围：2026-08-18 09:00 ~ 2026-08-19 08:40，共 12 条】\nAlice 和 Bob 敲定了周五上线。"
	messages := []Message{
		{Role: RoleSystem, Content: "人设", Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: summary, Priority: MessagePrioritySummary, AtomicText: true},
		{Role: RoleUser, Content: "现在的问题", Priority: MessagePriorityCurrent},
	}
	fitted := fitMessagesToTokenBudget(messages, 16384)

	found := false
	for _, message := range fitted {
		if message.Priority == MessagePrioritySummary {
			found = true
			if message.Content != summary {
				t.Fatalf("summary was rewritten: %q", message.Content)
			}
		}
	}
	if !found {
		t.Fatal("summary that fits was dropped")
	}
}

func TestMaxContextTokensFollowsTheModelWindow(t *testing.T) {
	cases := []struct {
		name        string
		window      int64
		maxContext  int64
		wantContext int64
	}{
		{name: "large window is not clamped to the 16K fallback", window: 200000, wantContext: 200000},
		{name: "unknown window falls back", window: 0, wantContext: DefaultContextWindowTokens},
		{name: "small model window wins", window: 8192, wantContext: 8192},
		{name: "explicit cap is preserved", window: 200000, maxContext: 32768, wantContext: 32768},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cfg := ProviderConfig{
				Provider:            ProviderOpenAICompatible,
				Model:               "test-model",
				ContextWindowTokens: item.window,
				MaxContextTokens:    item.maxContext,
			}.WithDefaults()
			if got := cfg.MaxContextTokensWithDefault(); got != item.wantContext {
				t.Fatalf("MaxContextTokensWithDefault = %d, want %d", got, item.wantContext)
			}
		})
	}
}

func TestLargeWindowGrowsTheInputBudget(t *testing.T) {
	small := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "m", ContextWindowTokens: 16384}.WithDefaults()
	large := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "m", ContextWindowTokens: 200000}.WithDefaults()

	smallBudget := InputTokenBudget(small.MaxContextTokensWithDefault(), DefaultMaxOutputTokens)
	largeBudget := InputTokenBudget(large.MaxContextTokensWithDefault(), DefaultMaxOutputTokens)
	if largeBudget <= smallBudget {
		t.Fatalf("a 200K model must get more input budget than a 16K one: %d vs %d", largeBudget, smallBudget)
	}
}
