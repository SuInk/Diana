// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type usageCountingProvider struct {
	calls int
}

func (p *usageCountingProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	return &llm.GenerateResponse{
		Provider: llm.ProviderOpenAICompatible,
		Model:    "gp5.5",
		Text:     "ok",
		Usage:    llm.Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
	}, nil
}

func usageEntriesFor(logs *captureAppLogs, messageID string) []map[string]any {
	out := make([]map[string]any, 0)
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action != "chatbot.llm_usage" || entry.Target != messageID {
			continue
		}
		out = append(out, entry.Metadata)
	}
	return out
}

// 一条消息可能触发路由、子任务、主生成好几次模型调用。记账挂在 provider 装饰链
// 上而不是逐个调用点手写，就是为了让这些都算进同一条消息的总用量。
func TestLLMUsageAccountingRecordsEveryCallUnderOneMessage(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "m1"}

	ctx := withLLMUsageContext(context.Background(), event)
	provider := &usageCountingProvider{}

	for _, purpose := range []string{"proactive_reply_router", "subagent", "reply"} {
		callCtx := withLLMUsagePurpose(ctx, purpose)
		run := runtime.withLLMUsageAccountingRun(callCtx, func(client LLMProvider) (string, error) {
			_, err := client.Generate(callCtx, llm.GenerateRequest{})
			return "", err
		})
		if _, err := run(provider); err != nil {
			t.Fatal(err)
		}
	}

	entries := usageEntriesFor(logs, "m1")
	if len(entries) != 3 {
		t.Fatalf("recorded %d usage entries, want one per call", len(entries))
	}
	seen := map[string]bool{}
	var total int64
	for _, metadata := range entries {
		purpose, _ := metadata["purpose"].(string)
		seen[purpose] = true
		if tokens, ok := metadata["total_tokens"].(int64); ok {
			total += tokens
		}
	}
	for _, want := range []string{"proactive_reply_router", "subagent", "reply"} {
		if !seen[want] {
			t.Fatalf("purpose %q was not recorded: %#v", want, entries)
		}
	}
	if total != 42 {
		t.Fatalf("total tokens = %d, want 42 across three calls", total)
	}
}

// 没有消息上下文时不记账，也不能崩：定时任务那些路径会自己补上下文。
func TestLLMUsageAccountingSkipsWithoutMessageContext(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)

	provider := &usageCountingProvider{}
	run := runtime.withLLMUsageAccountingRun(context.Background(), func(client LLMProvider) (string, error) {
		_, err := client.Generate(context.Background(), llm.GenerateRequest{})
		return "", err
	})
	if _, err := run(provider); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want the call to still go through", provider.calls)
	}
	if entries := usageEntriesFor(logs, ""); len(entries) != 0 {
		t.Fatalf("unexpected usage entries: %#v", entries)
	}
}

// 调用点没打标签时退回按请求形状推断，不能记成空 purpose。
func TestLLMUsageAccountingFallsBackToInferredPurpose(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "m2"}

	ctx := withLLMUsageContext(context.Background(), event)
	run := runtime.withLLMUsageAccountingRun(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{})
		return "", err
	})
	if _, err := run(&usageCountingProvider{}); err != nil {
		t.Fatal(err)
	}
	entries := usageEntriesFor(logs, "m2")
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if purpose, _ := entries[0]["purpose"].(string); purpose == "" {
		t.Fatalf("purpose must never be empty: %#v", entries[0])
	}
}
