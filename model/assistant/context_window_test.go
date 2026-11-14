// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRequestContextLimitDefaultsAndOverrides(t *testing.T) {
	const groupID = "35791"
	for _, item := range []struct {
		name       string
		botLimit   int64
		groupLimit int64
		want       int64
	}{
		{name: "都留空跟随模型窗口", want: 0},
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
			// 没有配置档时窗口兜底是 128K，显式上限把它收到配置值，留空就跟随窗口。
			want := item.want
			if want == 0 {
				want = llm.DefaultContextWindowTokens
			}
			if window := runtime.promptContextWindowTokens(event, cfg); window != want {
				t.Fatalf("window = %d, want %d", window, want)
			}
		})
	}
}

// 压缩阈值照 Pi：窗口减 16384；历史候选条数按阈值换算，盖住锚之后的全部历史。
func TestCompactionThresholdFollowsPi(t *testing.T) {
	for _, item := range []struct{ window, threshold int64 }{
		{window: 128000, threshold: 111616},
		{window: 32000, threshold: 15616},
		{window: 0, threshold: llm.DefaultContextWindowTokens - 16384},
		{window: 8000, threshold: 1},
	} {
		if got := compactionThreshold(item.window); got != item.threshold {
			t.Fatalf("window %d: threshold = %d, want %d", item.window, got, item.threshold)
		}
	}
	if got := historyCandidateLimit(128000); got != int(111616/minimumHistoryCandidateTokens)+8 {
		t.Fatalf("candidate limit = %d", got)
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
