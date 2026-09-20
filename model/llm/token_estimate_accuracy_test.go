// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// token 估算器的精度回归。
//
// 估算器只看字符类别，不看具体内容，所以样本只记录三类字符的数量、消息条数和供应
// 商回报的 input_tokens——聊天原文不入库，也不需要入库。样本取自线上 1223 条无缓
// 存请求，按 token 量级分层抽样 131 条，覆盖 747 到 70343。
//
// 这个测试挡住的是一类具体事故：估算器一旦重新开始高估，请求会在装得下的时候被判
// 成超预算，触发摘要压缩、丢图和历史改写，把 prompt cache 全打掉。单元测试用几个
// 手写字符串量不出这种偏差，只有拿真实分布对账才量得出来。

type tokenEstimateSample struct {
	ASCII       int   `json:"ascii"`
	BMP         int   `json:"bmp"`
	Astral      int   `json:"astral"`
	Messages    int   `json:"messages"`
	InputTokens int64 `json:"input_tokens"`
}

func loadTokenEstimateSamples(t *testing.T) []tokenEstimateSample {
	t.Helper()
	raw, err := os.ReadFile("testdata/token_estimate_samples.json")
	if err != nil {
		t.Fatalf("read samples: %v", err)
	}
	var file struct {
		Samples []tokenEstimateSample `json:"samples"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode samples: %v", err)
	}
	if len(file.Samples) == 0 {
		t.Fatal("no samples")
	}
	return file.Samples
}

// synthesizeSample 按样本的字符构成拼出一段文本。真实请求里中英是交替出现的（中文
// 正文夹着 ID、时间戳和 JSON），所以这里也按小段交替，让连续段取整的行为和线上一
// 致，而不是把同类字符堆成一整块。
func synthesizeSample(sample tokenEstimateSample) string {
	const chunk = 24
	var builder strings.Builder
	ascii, bmp, astral := sample.ASCII, sample.BMP, sample.Astral
	for ascii > 0 || bmp > 0 || astral > 0 {
		for i := 0; i < chunk && ascii > 0; i++ {
			builder.WriteByte('a')
			ascii--
		}
		for i := 0; i < chunk && bmp > 0; i++ {
			builder.WriteRune('中')
			bmp--
		}
		if astral > 0 {
			builder.WriteRune('🙂')
			astral--
		}
	}
	return builder.String()
}

func tokenEstimateDeviations(samples []tokenEstimateSample, minTokens int64) []float64 {
	deviations := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.InputTokens < minTokens {
			continue
		}
		estimate := estimateTextTokens(synthesizeSample(sample)) +
			messageTokenOverhead*int64(sample.Messages)
		deviations = append(deviations, float64(estimate-sample.InputTokens)/float64(sample.InputTokens))
	}
	sort.Float64s(deviations)
	return deviations
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * q)
	return sorted[index]
}

func TestTokenEstimateTracksProviderUsage(t *testing.T) {
	samples := loadTokenEstimateSamples(t)
	deviations := tokenEstimateDeviations(samples, 0)

	median := quantile(deviations, 0.5)
	p90 := quantile(deviations, 0.9)

	t.Logf("relative deviation: median=%+.1f%% p90=%+.1f%% (n=%d)", 100*median, 100*p90, len(deviations))

	// 偏差必须落在「略微保守」这个窄带里：上界防假超限，下界防真超限。
	if median < 0 || median > 0.25 {
		t.Errorf("median deviation %+.1f%% outside [0%%, +25%%]", 100*median)
	}
	if p90 > 0.40 {
		t.Errorf("p90 deviation %+.1f%% exceeds +40%%", 100*p90)
	}
}

// 低估只在请求逼近上下文上限时才有后果，所以下界用够大的请求来量。
//
// 小请求里有一档估算器天生量不准：内容几乎全是随机 hex（会话别名 im_xxxxxxxxxxxx
// 这类），实际分词率能到每字符一个以上 token，而 ASCII 档按 3.3 字符一个 token
// 算，最深能低估一半。线上这类样本占千分之三，且都在一万 token 以下——离 12.8 万
// 的预算差两个数量级，低估不会造成任何超限。真正会顶到预算的大请求里没有这种成
// 分，最差低估 -4.1%。要收掉这个尾巴得在估算器里识别高熵 ASCII 段，成本远高于收
// 益；这里把它写明，并用分档断言把真正重要的那一档钉死。
func TestLargeRequestEstimateDoesNotUnderShoot(t *testing.T) {
	const floor = 10_000
	samples := loadTokenEstimateSamples(t)
	deviations := tokenEstimateDeviations(samples, floor)
	if len(deviations) < 20 {
		t.Fatalf("only %d samples at or above %d tokens", len(deviations), floor)
	}

	worstUnder := deviations[0]
	t.Logf("requests >= %d tokens: worst underestimate %+.1f%% (n=%d)", floor, 100*worstUnder, len(deviations))

	if worstUnder < -0.15 {
		t.Errorf("worst underestimate %+.1f%% below -15%%", 100*worstUnder)
	}

	var under int
	for _, d := range deviations {
		if d < 0 {
			under++
		}
	}
	if share := float64(under) / float64(len(deviations)); share > 0.25 {
		t.Errorf("%.0f%% of large samples underestimate, want <=25%%", 100*share)
	}
}

// 中文按每字 2 token 计是这次修复的起因，单独钉住这个系数，避免被「保守一点更安
// 全」的直觉改回去。
func TestWideCharacterTokenRate(t *testing.T) {
	const runes = 800
	got := estimateTextTokens(strings.Repeat("中", runes))
	rate := float64(got) / runes
	if rate < 0.75 || rate > 1.0 {
		t.Fatalf("wide-character rate %.3f token/rune outside [0.75, 1.00]", rate)
	}
	if rate >= 1.5 {
		t.Fatalf("wide characters priced at %.2f token/rune: the 2.0 regression is back", rate)
	}
}

func TestASCIITokenRateStaysConservative(t *testing.T) {
	const runes = 900
	got := estimateTextTokens(strings.Repeat("a", runes))
	rate := float64(got) / runes
	// 实测 0.24，取 1/3 留余量；低于实测就会开始真超限。
	if rate < 0.30 || rate > 0.40 {
		t.Fatalf("ascii rate %.3f token/rune outside [0.30, 0.40]", rate)
	}
}

// 连续同类段累加再取整，不能逐字向上取整——后者会把偏差重新放大回去。
func TestEstimateDoesNotRoundPerRune(t *testing.T) {
	long := estimateTextTokens(strings.Repeat("中", 80))
	var split int64
	for i := 0; i < 80; i++ {
		split += estimateTextTokens("中")
	}
	if long >= split {
		t.Fatalf("segment accumulation (%d) should beat per-rune rounding (%d)", long, split)
	}
}

func TestContinuationStateCountsTowardBudget(t *testing.T) {
	reasoning := strings.Repeat("x", 3000)
	base := estimateMessageTokens(Message{Role: RoleAssistant, Content: "ok"})
	withState := estimateMessageTokens(Message{
		Role:             RoleAssistant,
		Content:          "ok",
		ReasoningContent: &reasoning,
		ResponsesOutput:  []json.RawMessage{json.RawMessage(fmt.Sprintf("%q", reasoning))},
	})
	if withState <= base {
		t.Fatal("reasoning and Responses continuation state must count toward the input budget")
	}
}
