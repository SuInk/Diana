// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// blockingLLMProvider 在 Generate 里停住，让测试能在「调用还没回来」的那一刻读状态。
type blockingLLMProvider struct {
	model   string
	entered chan struct{}
	release chan struct{}
}

func (p *blockingLLMProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.entered <- struct{}{}
	<-p.release
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: p.model, Text: "ok"}, nil
}

// 模型并发要在调用进行中就能读到，调用结束后要归零；峰值留着不回落。
func TestRuntimeStatusReportsInFlightLLMCalls(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if active := runtime.Status().LLMConcurrency.Active; active != 0 {
		t.Fatalf("idle runtime reports %d in-flight llm calls, want 0", active)
	}

	providers := []*blockingLLMProvider{
		{model: "gpt-5.6-terra", entered: make(chan struct{}), release: make(chan struct{})},
		{model: "gpt-5.6-terra", entered: make(chan struct{}), release: make(chan struct{})},
		{model: "claude-sonnet-5", entered: make(chan struct{}), release: make(chan struct{})},
	}
	var wg sync.WaitGroup
	for _, provider := range providers {
		wg.Add(1)
		go func(provider *blockingLLMProvider) {
			defer wg.Done()
			ctx := withLLMUsagePurpose(context.Background(), "reply")
			run := runtime.withLLMUsageAccountingRun(ctx, func(client LLMProvider) (string, error) {
				_, err := client.Generate(ctx, llm.GenerateRequest{Model: provider.model})
				return "", err
			})
			if _, err := run(provider); err != nil {
				t.Error(err)
			}
		}(provider)
	}
	for _, provider := range providers {
		<-provider.entered
	}

	status := runtime.Status().LLMConcurrency
	if status.Active != len(providers) {
		t.Fatalf("active = %d, want %d", status.Active, len(providers))
	}
	if status.Peak != len(providers) {
		t.Fatalf("peak = %d, want %d", status.Peak, len(providers))
	}
	if len(status.Models) != 2 {
		t.Fatalf("models = %d, want one entry per distinct model", len(status.Models))
	}
	// 明细按并发数降序：两次 terra 排在一次 sonnet 前面。
	if status.Models[0].Model != "gpt-5.6-terra" || status.Models[0].Active != 2 {
		t.Fatalf("first model entry = %+v, want gpt-5.6-terra with 2 calls", status.Models[0])
	}
	if status.Models[1].Model != "claude-sonnet-5" || status.Models[1].Active != 1 {
		t.Fatalf("second model entry = %+v, want claude-sonnet-5 with 1 call", status.Models[1])
	}
	if status.Models[0].StartedAt.IsZero() {
		t.Fatal("model entry is missing the start time of its oldest in-flight call")
	}

	for _, provider := range providers {
		close(provider.release)
	}
	wg.Wait()

	status = runtime.Status().LLMConcurrency
	if status.Active != 0 || len(status.Models) != 0 {
		t.Fatalf("after completion active = %d with %d model entries, want 0", status.Active, len(status.Models))
	}
	if status.Peak != len(providers) {
		t.Fatalf("peak = %d after completion, want it to keep the high-water mark %d", status.Peak, len(providers))
	}
}

// 一次逻辑调用只占一个并发位：内层再套一层记账装饰器（重试、配置档降级都会走到）
// 不该把同一次调用数成两次。
func TestLLMConcurrencyCountsOneSlotPerLogicalCall(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	inner := &blockingLLMProvider{model: "gpt-5.6-terra", entered: make(chan struct{}), release: make(chan struct{})}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()
		nested := &usageAccountingLLMProvider{runtime: runtime, state: &llmUsageState{}, provider: inner}
		run := runtime.withLLMUsageAccountingRun(ctx, func(client LLMProvider) (string, error) {
			_, err := client.Generate(ctx, llm.GenerateRequest{})
			return "", err
		})
		if _, err := run(nested); err != nil {
			t.Error(err)
		}
	}()
	<-inner.entered

	if active := runtime.Status().LLMConcurrency.Active; active != 1 {
		t.Fatalf("active = %d for a doubly decorated call, want 1", active)
	}
	close(inner.release)
	<-done
}

// token 合计跟着每一次记账走：今日桶跨日清零，本次运行那一桶不清。
func TestRuntimeStatusAccumulatesLLMTokenUsage(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	day := time.Date(2026, 9, 20, 23, 30, 0, 0, time.Local)
	runtime.now = func() time.Time { return day }

	ctx := context.Background()
	runtime.recordLLMUsage(ctx, MessageEvent{}, llm.ProviderOpenAICompatible, "gpt-5.6-terra", llm.Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 60, TotalTokens: 120}, "reply", time.Second, 0)
	// 上游没报用量的那一次也要数进调用次数，只是 token 记不上。
	runtime.recordLLMUsage(ctx, MessageEvent{}, llm.ProviderOpenAICompatible, "gpt-5.6-terra", llm.Usage{}, "reply", time.Second, 0)

	usage := runtime.Status().LLMUsage
	if usage.Today.Calls != 2 || usage.Session.Calls != 2 {
		t.Fatalf("calls today=%d session=%d, want 2 and 2", usage.Today.Calls, usage.Session.Calls)
	}
	if usage.Today.TotalTokens != 120 || usage.Today.InputTokens != 100 || usage.Today.OutputTokens != 20 || usage.Today.CachedInputTokens != 60 {
		t.Fatalf("today counters = %+v, want the single reported call's usage", usage.Today)
	}
	if usage.Today.MissingUsageCalls != 1 {
		t.Fatalf("missing usage calls = %d, want the unreported call to be flagged", usage.Today.MissingUsageCalls)
	}

	runtime.now = func() time.Time { return day.Add(time.Hour) }
	runtime.recordLLMUsage(ctx, MessageEvent{}, llm.ProviderOpenAICompatible, "gpt-5.6-terra", llm.Usage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10}, "reply", time.Second, 0)

	usage = runtime.Status().LLMUsage
	if usage.Today.Calls != 1 || usage.Today.TotalTokens != 10 {
		t.Fatalf("today counters = %+v after the date rolled over, want only the new call", usage.Today)
	}
	if usage.Session.Calls != 3 || usage.Session.TotalTokens != 130 {
		t.Fatalf("session counters = %+v, want every call since start", usage.Session)
	}
}
