// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"math"
	"testing"
)

// TestTokenTotalsRateAggregatesBeforeDividing 聚合速率必须先加总再除。
//
// 最容易写错的是「把每次调用的速率平均一下」。一次 2000 token 的生成（100 tok/s）
// 和一次 5 token 的分类（5 tok/s）平均出来是 52.5 tok/s——这个数不对应任何真实
// 现象。正确的是 (2000+5) / (20+1) 秒。
func TestTokenTotalsRateAggregatesBeforeDividing(t *testing.T) {
	var totals inboundEventTokenTotals
	totals.add(inboundEventTokenTotals{LLMCalls: 1, OutputTokens: 2000, DurationMS: 20000})
	totals.add(inboundEventTokenTotals{LLMCalls: 1, OutputTokens: 5, DurationMS: 1000})

	if totals.DurationMS != 21000 || totals.OutputTokens != 2005 {
		t.Fatalf("加总不对：%+v", totals)
	}
	want := 2005.0 / 21.0
	if got := totals.tokensPerSecond(); math.Abs(got-want) > 0.02 {
		t.Fatalf("聚合速率 = %v，应约为 %v（先加总再除，不是把两个速率平均）", got, want)
	}
	// 明确排除「平均速率」这个错误答案。
	if avg := (100.0 + 5.0) / 2; math.Abs(totals.tokensPerSecond()-avg) < 1 {
		t.Fatalf("聚合速率退化成了各次调用速率的平均值：%v", totals.tokensPerSecond())
	}
}

// TestTokenTotalsRateHandlesLegacyRows 老日志没有 duration_ms，速率给 0 而不是错的数。
//
// 这个字段是后加的，历史行里没有。耗时为 0 时如果不挡住，除出来是 +Inf，
// JSON 序列化会失败，整个事件列表接口会挂。
func TestTokenTotalsRateHandlesLegacyRows(t *testing.T) {
	legacy := inboundEventTokenTotals{LLMCalls: 3, OutputTokens: 900}
	if got := legacy.tokensPerSecond(); got != 0 {
		t.Fatalf("没有耗时的老数据速率 = %v，应为 0", got)
	}
	if math.IsInf(legacy.tokensPerSecond(), 0) || math.IsNaN(legacy.tokensPerSecond()) {
		t.Fatal("速率算出了 Inf/NaN，JSON 序列化会失败")
	}
}
