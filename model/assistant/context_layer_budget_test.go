// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"testing"
)

func TestContextLayerBudgetsCapLargeWindows(t *testing.T) {
	const window int64 = 128000

	// 近期历史在大窗口下压在 recentHistoryKeepCeiling；其余几层被各自的上限压住。
	if got := recentHistoryBudget(window); got != recentHistoryKeepCeiling {
		t.Fatalf("history budget = %d, want the keep ceiling %d", got, recentHistoryKeepCeiling)
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
	const window int64 = 8000

	// 小窗口下绝对值是上限不是下限，仍按份额走，不能超发。近期历史扣掉预留后已经
	// 不剩，落到最小保底。
	if got := recentHistoryBudget(window); got != minimumRecentHistoryTokens {
		t.Fatalf("history budget = %d, want the floor %d", got, minimumRecentHistoryTokens)
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

// 机器人和群都留空时上限落到 64K；填了谁就用谁，群优先。近期历史只由收紧后的窗口
// 算出，不再有单独的历史预算。
func TestRequestContextLimitDefaultsAndOverrides(t *testing.T) {
	const groupID = "35791"
	for _, item := range []struct {
		name       string
		botLimit   int64
		groupLimit int64
		want       int64
	}{
		{name: "都留空", want: DefaultRequestContextTokens},
		{name: "机器人填了", botLimit: 32000, want: 32000},
		{name: "群填了", botLimit: 32000, groupLimit: 12000, want: 12000},
		{name: "只有群填了", groupLimit: 48000, want: 48000},
	} {
		t.Run(item.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{MaxContextTokens: item.botLimit}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
				groupID: {GroupID: groupID, MaxContextTokens: item.groupLimit},
			}})
			event := MessageEvent{Kind: EventKindGroup, GroupID: groupID}
			cfg := runtime.effectiveConfigForEvent(event)
			if got := cfg.requestContextLimit(); got != item.want {
				t.Fatalf("limit = %d, want %d", got, item.want)
			}
			// 没有配置档时窗口兜底是 128K，上限把它收到配置值。
			window := runtime.promptContextWindowTokens(event, cfg)
			if window != item.want {
				t.Fatalf("window = %d, want %d", window, item.want)
			}
			if got, want := recentHistoryBudget(window), max(minimumRecentHistoryTokens, min(recentHistoryKeepCeiling, (item.want-requestContextReserveTokens)/2)); got != want {
				t.Fatalf("history budget = %d, want %d", got, want)
			}
		})
	}
}

// 照雨夹雪分历史：重新组装只带 min(20000, (上限−16384)/2)，续接涨到上限减 16384。
func TestRecentHistoryKeepsRecentSliceAndAppendGrowsToReserve(t *testing.T) {
	for _, item := range []struct {
		limit, history, appendLimit int64
	}{
		// (64000−16384)/2 = 23808，压到 20000。
		{limit: DefaultRequestContextTokens, history: 20000, appendLimit: 47616},
		{limit: 32000, history: 7808, appendLimit: 15616},
		{limit: 128000, history: 20000, appendLimit: 111616},
		// 上限比预留还小：历史落到保底，续接上限取 1（每轮重新组装），不能落成表示不限的 0。
		{limit: 12000, history: minimumRecentHistoryTokens, appendLimit: 1},
	} {
		if got := recentHistoryBudget(item.limit); got != item.history {
			t.Fatalf("limit %d: history budget = %d, want %d", item.limit, got, item.history)
		}
		if got := appendContextLimit(item.limit); got != item.appendLimit {
			t.Fatalf("limit %d: append limit = %d, want %d", item.limit, got, item.appendLimit)
		}
	}
	// 0 表示不限，续接也不限。
	if got := appendContextLimit(0); got != 0 {
		t.Fatalf("append limit for unlimited = %d", got)
	}
}

// 旧数据库和旧前端提交里还会带着已删掉的 recent_history_token_budget。存库读取
// （loadJSON 的 json.Unmarshal）和 WebUI 保存（gin ShouldBindJSON）都不拒绝未知键，
// 这里钉住：读进来不报错，其余字段照常生效。
func TestLegacyRecentHistoryTokenBudgetKeyIsIgnored(t *testing.T) {
	raw := []byte(`{"max_context_tokens":24000,"recent_history_token_budget":16000}`)
	var bot BotConfig
	if err := json.Unmarshal(raw, &bot); err != nil || bot.MaxContextTokens != 24000 {
		t.Fatalf("bot = %+v, err = %v", bot, err)
	}
	var group GroupConfig
	if err := json.Unmarshal(raw, &group); err != nil || group.MaxContextTokens != 24000 {
		t.Fatalf("group = %+v, err = %v", group, err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.MaxContextTokens != 24000 {
		t.Fatalf("payload = %+v, err = %v", payload, err)
	}
}
