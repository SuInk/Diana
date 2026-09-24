// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

type usageSinceStub struct {
	since, until time.Time
	summary      applog.UsageSummary
}

func (s *usageSinceStub) LLMUsageSince(_ context.Context, since, until time.Time) (applog.UsageSummary, error) {
	s.since, s.until = since, until
	return s.summary, nil
}

// 早上九点半重启过，总览页的今日 Token 就只剩九点半以后的量。启动时从库里把零点
// 到现在的用量垫回来，之后的调用接着往上加。
func TestRestoreLLMUsageTodayFromLogs(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 9, 24, 9, 31, 43, 0, loc)
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	stub := &usageSinceStub{summary: applog.UsageSummary{Calls: 300, InputTokens: 9_000_000, OutputTokens: 100_000, CachedInputTokens: 5_000_000, TotalTokens: 9_100_000}}

	if err := runtime.RestoreLLMUsageToday(context.Background(), stub); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 24, 0, 0, 0, 0, loc); !stub.since.Equal(want) || !stub.until.Equal(now) {
		t.Fatalf("窗口 = [%s, %s)，应当从本地零点到现在", stub.since, stub.until)
	}
	runtime.recordLLMUsageTotals(llm.Usage{InputTokens: 1000, OutputTokens: 10, TotalTokens: 1010})
	totals := runtime.llmUsageTotals()
	if totals.Today.Calls != 301 || totals.Today.TotalTokens != 9_101_010 || totals.Today.CachedInputTokens != 5_000_000 {
		t.Fatalf("today = %+v", totals.Today)
	}
	// 本次运行累计只数启动以后的。
	if totals.Session.Calls != 1 || totals.Session.TotalTokens != 1010 {
		t.Fatalf("session = %+v", totals.Session)
	}

	// 过了零点还没有新调用：今日应当显示 0，而不是一直挂着昨天的数。
	now = time.Date(2026, 9, 25, 0, 5, 0, 0, loc)
	if got := runtime.llmUsageTotals().Today; got.Calls != 0 || got.TotalTokens != 0 {
		t.Fatalf("跨日后 today = %+v", got)
	}
	runtime.recordLLMUsageTotals(llm.Usage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10})
	if got := runtime.llmUsageTotals().Today; got.Calls != 1 || got.TotalTokens != 10 {
		t.Fatalf("跨日后第一次调用 today = %+v", got)
	}
}

// 已经有调用累加进来之后再垫，会把那些调用数两遍；这种情况不垫。
func TestRestoreLLMUsageTodayAfterCallsIsNoop(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.recordLLMUsageTotals(llm.Usage{TotalTokens: 10})
	if err := runtime.RestoreLLMUsageToday(context.Background(), &usageSinceStub{summary: applog.UsageSummary{Calls: 99, TotalTokens: 999}}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.llmUsageTotals().Today; got.Calls != 1 || got.TotalTokens != 10 {
		t.Fatalf("today = %+v", got)
	}
}
