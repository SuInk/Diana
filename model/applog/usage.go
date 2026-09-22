package applog

import (
	"context"
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
}

type UsageReader interface {
	LLMUsageSince(context.Context, time.Time, time.Time) (UsageSummary, error)
}

// GroupUsage 是一个群在窗口内的用量。token 和次数分开给：两种超用的形态不一样，
// 一个群可以句句短但刷个不停，也可以只说几句却每句都带图。
type GroupUsage struct {
	Tokens int64 `json:"tokens"`
	Calls  int64 `json:"calls"`
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
