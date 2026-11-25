package applog

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

// UsageSummary counts recorded calls across this Diana instance.
// CachedInputTokens is already included in InputTokens.
type UsageSummary struct {
	Since             time.Time `json:"since"`
	Until             time.Time `json:"until"`
	Calls             int64     `json:"recorded_calls"`
	InputTokens       int64     `json:"input_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	TotalTokens       int64     `json:"total_tokens"`
	CachedInputTokens int64     `json:"cached_input_tokens"`
	MissingUsageCalls int64     `json:"usage_missing_calls,omitempty"`
	// FailedCalls 是窗口内失败的模型请求数，不算在 Calls 里：Calls 只数成功的调用，
	// 降级链里失败的每一档各算一次。RecoveredCalls 是其中最终由后备模型接上的次数。
	FailedCalls           int64            `json:"failed_calls"`
	RecoveredCalls        int64            `json:"recovered_calls,omitempty"`
	DurationMS            int64            `json:"duration_ms,omitempty"`
	OutputTokensPerSecond float64          `json:"output_tokens_per_second,omitempty"`
	AvgTTFTMS             float64          `json:"avg_ttft_ms,omitempty"`
	TTFTCalls             int64            `json:"ttft_calls,omitempty"`
	Breakdown             []UsageBreakdown `json:"breakdown"`
	ttftSumMS             int64
}

// AddCall 把单次调用折算出的度量累加进汇总，并同步刷新吞吐和首 token 均值。
func (s *UsageSummary) AddCall(call UsageBreakdown) {
	s.Calls += call.Calls
	s.InputTokens += call.InputTokens
	s.OutputTokens += call.OutputTokens
	s.TotalTokens += call.TotalTokens
	s.CachedInputTokens += call.CachedInputTokens
	s.MissingUsageCalls += call.MissingUsageCalls
	s.FailedCalls += call.FailedCalls
	s.RecoveredCalls += call.RecoveredCalls
	s.DurationMS += call.DurationMS
	s.TTFTCalls += call.TTFTCalls
	s.ttftSumMS += callTTFTSum(call)
	s.OutputTokensPerSecond = computeTokensPerSecond(s.OutputTokens, s.DurationMS)
	s.AvgTTFTMS = computeAvgTTFT(s.ttftSumMS, s.TTFTCalls)
}

// UsageBreakdown preserves the combination of purpose and actual model so
// consumers can group by either dimension or inspect their intersection.
// CachedInputTokens is included in InputTokens, never added to TotalTokens.
type UsageBreakdown struct {
	Purpose               string  `json:"purpose"`
	Provider              string  `json:"provider"`
	Model                 string  `json:"model"`
	Calls                 int64   `json:"recorded_calls"`
	InputTokens           int64   `json:"input_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
	CachedInputTokens     int64   `json:"cached_input_tokens"`
	MissingUsageCalls     int64   `json:"missing_usage_calls"`
	FailedCalls           int64   `json:"failed_calls"`
	RecoveredCalls        int64   `json:"recovered_calls,omitempty"`
	DurationMS            int64   `json:"duration_ms,omitempty"`
	OutputTokensPerSecond float64 `json:"output_tokens_per_second,omitempty"`
	AvgTTFTMS             float64 `json:"avg_ttft_ms,omitempty"`
	TTFTCalls             int64   `json:"ttft_calls,omitempty"`
	ttftSumMS             int64
}

// SetTTFTSample 在构造单次调用样本时记录流式首 token 时延（毫秒）。
func (b *UsageBreakdown) SetTTFTSample(ttftMS int64) {
	if ttftMS <= 0 {
		return
	}
	b.ttftSumMS = ttftMS
	b.TTFTCalls = 1
	b.AvgTTFTMS = float64(ttftMS)
}

func callTTFTSum(entry UsageBreakdown) int64 {
	if entry.ttftSumMS > 0 {
		return entry.ttftSumMS
	}
	if entry.TTFTCalls > 0 && entry.AvgTTFTMS > 0 {
		return int64(math.Round(entry.AvgTTFTMS * float64(entry.TTFTCalls)))
	}
	return 0
}

func computeTokensPerSecond(outputTokens, durationMS int64) float64 {
	if outputTokens <= 0 || durationMS <= 0 {
		return 0
	}
	return math.Round((float64(outputTokens)*1000/float64(durationMS))*10) / 10
}

func computeAvgTTFT(sumMS, calls int64) float64 {
	if calls <= 0 || sumMS <= 0 {
		return 0
	}
	return math.Round((float64(sumMS)/float64(calls))*10) / 10
}

type usageBreakdownKey struct {
	purpose, provider, model string
}

// UsageBreakdownAccumulator is shared by runtime counters and durable log
// queries. Callers provide synchronization when used across goroutines.
type UsageBreakdownAccumulator struct {
	entries map[usageBreakdownKey]UsageBreakdown
}

func (a *UsageBreakdownAccumulator) Add(entry UsageBreakdown) {
	entry.Purpose = strings.TrimSpace(entry.Purpose)
	entry.Provider = strings.TrimSpace(entry.Provider)
	entry.Model = strings.TrimSpace(entry.Model)
	if entry.Purpose == "" {
		entry.Purpose = "unlabeled"
	}
	if entry.Model == "" {
		entry.Model = "unknown"
	}
	if a.entries == nil {
		a.entries = make(map[usageBreakdownKey]UsageBreakdown)
	}
	key := usageBreakdownKey{entry.Purpose, entry.Provider, entry.Model}
	current := a.entries[key]
	current.Purpose, current.Provider, current.Model = entry.Purpose, entry.Provider, entry.Model
	current.Calls += entry.Calls
	current.InputTokens += entry.InputTokens
	current.OutputTokens += entry.OutputTokens
	current.TotalTokens += entry.TotalTokens
	current.CachedInputTokens += entry.CachedInputTokens
	current.MissingUsageCalls += entry.MissingUsageCalls
	current.FailedCalls += entry.FailedCalls
	current.RecoveredCalls += entry.RecoveredCalls
	current.DurationMS += entry.DurationMS
	current.TTFTCalls += entry.TTFTCalls
	current.ttftSumMS += callTTFTSum(entry)
	current.OutputTokensPerSecond = computeTokensPerSecond(current.OutputTokens, current.DurationMS)
	current.AvgTTFTMS = computeAvgTTFT(current.ttftSumMS, current.TTFTCalls)
	a.entries[key] = current
}

func (a *UsageBreakdownAccumulator) Snapshot() []UsageBreakdown {
	entries := make([]UsageBreakdown, 0, len(a.entries))
	for _, entry := range a.entries {
		entry.ttftSumMS = 0
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		if a.Purpose != b.Purpose {
			return a.Purpose < b.Purpose
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	return entries
}

type UsageReader interface {
	LLMUsageSince(context.Context, time.Time, time.Time) (UsageSummary, error)
}

// GroupUsage 是一个群在窗口内的用量：模型调用次数。
type GroupUsage struct {
	Calls int64 `json:"calls"`
}

// GroupUsageReader 是可选能力：按群统计窗口内的用量，供按群额度使用。
// 存储没实现它时额度功能自动失效（不限额），而不是把所有群都当成超额。
type GroupUsageReader interface {
	GroupLLMUsageSince(ctx context.Context, profileID, groupID string, since, until time.Time) (GroupUsage, error)
}

// GroupUsageBulkReader 一次问出一台机器人名下所有群的窗口用量。控制台要在群列表
// 里画进度条，逐群查会变成 N 次全表扫描；额度判断只关心一个群，两条路径各用各的。
type GroupUsageBulkReader interface {
	GroupLLMUsageSinceByProfile(ctx context.Context, profileID string, since, until time.Time) (map[string]GroupUsage, error)
}

// UsageFilter uses strict attribution: unlabelled historical calls are never
// silently assigned to a robot, platform or group.
type UsageFilter struct {
	ProfileID string
	Platform  string
	GroupID   string
}
type GroupTokenUsage struct {
	ProfileID string                  `json:"profile_id,omitempty"`
	Platform  string                  `json:"platform,omitempty"`
	GroupID   string                  `json:"group_id"`
	Usage     UsageSummary            `json:"usage"`
	Purposes  map[string]UsageSummary `json:"purposes"`
}
type UsageReport struct {
	Usage  UsageSummary      `json:"usage"`
	Groups []GroupTokenUsage `json:"groups"`
}
type UsageReportReader interface {
	LLMUsageReport(context.Context, UsageFilter, time.Time, time.Time) (UsageReport, error)
}
