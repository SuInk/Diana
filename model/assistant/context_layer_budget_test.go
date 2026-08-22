// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestContextLayerBudgetsCapLargeWindows(t *testing.T) {
	cfg := DefaultBotConfig()
	const window int64 = 128000

	// 55% × 128K = 70,400。绝对上限把它按回 16K：历史的价值曲线衰减极快，
	// 再往前是白付 prefill 的钱和延迟。
	if got := recentHistoryBudget(window, cfg); got != DefaultRecentHistoryTokenBudget {
		t.Fatalf("history budget = %d, want %d", got, DefaultRecentHistoryTokenBudget)
	}
	if got := sessionThreadBudget(window); got != sessionThreadTokenCeiling {
		t.Fatalf("thread budget = %d", got)
	}
	if got := retrievedMemoryBudget(window); got != retrievedMemoryTokenCeiling {
		t.Fatalf("retrieved memory budget = %d", got)
	}
	if got := coreMemoryBudget(window); got != coreMemoryTokenCeiling {
		t.Fatalf("core memory budget = %d", got)
	}
}

func TestContextLayerBudgetsFollowShareOnSmallWindows(t *testing.T) {
	cfg := DefaultBotConfig()
	const window int64 = 8000

	// 小窗口下绝对值是上限不是下限，仍按份额走，不能超发。
	if got := recentHistoryBudget(window, cfg); got != 4400 {
		t.Fatalf("history budget = %d, want 4400", got)
	}
	if got := retrievedMemoryBudget(window); got != 800 {
		t.Fatalf("retrieved memory budget = %d, want 800", got)
	}
	// 两个「固定」项在小窗口下必须跟着缩，否则合起来会把历史挤到墙角。
	if got := coreMemoryBudget(window); got != 400 {
		t.Fatalf("core memory budget = %d, want 400", got)
	}
	if got := sessionThreadBudget(window); got != 1200 {
		t.Fatalf("thread budget = %d, want 1200", got)
	}
}

func TestRecentHistoryBudgetHonoursConfigButCannotExceedShare(t *testing.T) {
	cfg := DefaultBotConfig()

	cfg.RecentHistoryTokenBudget = 4000
	if got := recentHistoryBudget(128000, cfg); got != 4000 {
		t.Fatalf("configured budget = %d, want 4000", got)
	}

	// 配置只能收紧不能放宽：填得比窗口份额还大时仍按份额走。
	cfg.RecentHistoryTokenBudget = 900000
	if got := recentHistoryBudget(128000, cfg); got != 70400 {
		t.Fatalf("oversized budget = %d, want the 55%% share 70400", got)
	}

	cfg.RecentHistoryTokenBudget = 0
	if got := recentHistoryBudget(128000, cfg); got != DefaultRecentHistoryTokenBudget {
		t.Fatalf("zero budget should fall back to the default, got %d", got)
	}
}
