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
