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

// GroupUsageReader 是可选能力：按群统计窗口内的 token 用量，供按群额度使用。
// 存储没实现它时额度功能自动失效（不限额），而不是把所有群都当成超额。
type GroupUsageReader interface {
	GroupLLMTokensSince(ctx context.Context, profileID, groupID string, since, until time.Time) (int64, error)
}
