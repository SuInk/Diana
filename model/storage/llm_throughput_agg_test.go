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

// TestTTFTAverageIgnoresCallsWithoutSamples 没有 TTFT 的调用不能当 0 参与平均。
//
// 没开流式、或底层退化成非流式的调用不写 ttft_ms。把它们算成 0 会把均值稀释成
// 一个假的小数字——看起来首 token 特别快，实际只是大部分调用没有样本。
func TestTTFTAverageIgnoresCallsWithoutSamples(t *testing.T) {
	var totals inboundEventTokenTotals
	totals.add(inboundEventTokenTotals{LLMCalls: 1, TTFTSumMS: 400, TTFTCalls: 1})
	totals.add(inboundEventTokenTotals{LLMCalls: 1}) // 没有样本
	totals.add(inboundEventTokenTotals{LLMCalls: 1}) // 没有样本

	if totals.TTFTCalls != 1 {
		t.Fatalf("样本数 = %d，只有一次调用带了 TTFT", totals.TTFTCalls)
	}
	if got := totals.avgTTFTMS(); got != 400 {
		t.Fatalf("TTFT 均值 = %v，应为 400（不是 400/3）", got)
	}
}

// TestTTFTAverageWithoutAnySample 一个样本都没有时给 0，界面据此不显示这一项。
func TestTTFTAverageWithoutAnySample(t *testing.T) {
	totals := inboundEventTokenTotals{LLMCalls: 5, OutputTokens: 900, DurationMS: 3000}
	if got := totals.avgTTFTMS(); got != 0 {
		t.Fatalf("没有样本时 TTFT 均值 = %v，应为 0", got)
	}
}
