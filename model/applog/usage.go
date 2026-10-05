package applog

import (
	"context"
	"sort"
	"strings"
	"time"
)

// UsageSummary counts recorded calls across this Diana instance.
// CachedInputTokens is already included in InputTokens.
type UsageSummary struct {
	Since             time.Time        `json:"since"`
	Until             time.Time        `json:"until"`
	Calls             int64            `json:"recorded_calls"`
	InputTokens       int64            `json:"input_tokens"`
	OutputTokens      int64            `json:"output_tokens"`
	TotalTokens       int64            `json:"total_tokens"`
	CachedInputTokens int64            `json:"cached_input_tokens"`
	MissingUsageCalls int64            `json:"usage_missing_calls,omitempty"`
	Breakdown         []UsageBreakdown `json:"breakdown"`
}

// UsageBreakdown preserves the combination of purpose and actual model so
// consumers can group by either dimension or inspect their intersection.
// CachedInputTokens is included in InputTokens, never added to TotalTokens.
type UsageBreakdown struct {
	Purpose           string `json:"purpose"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	Calls             int64  `json:"recorded_calls"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	MissingUsageCalls int64  `json:"missing_usage_calls"`
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
	a.entries[key] = current
}

func (a *UsageBreakdownAccumulator) Snapshot() []UsageBreakdown {
	entries := make([]UsageBreakdown, 0, len(a.entries))
	for _, entry := range a.entries {
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
