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
		if entry.Action != "llm_usage" || entry.Target != messageID {
			continue
		}
		out = append(out, entry.Metadata)
	}
	return out
}

// withoutUsageEntries 去掉用量记录。现在每次模型调用都会记一条，只关心业务日志
// 的测试用它过滤，不必跟着调用次数改断言。
func withoutUsageEntries(entries []applog.Entry) []applog.Entry {
	out := make([]applog.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action != "llm_usage" {
			out = append(out, entry)
		}
	}
	return out
}

// withoutDebugTraceEntries 去掉调试轨迹。调试模式默认开着，每条消息开头都有一条
// 「收到消息」，只关心业务日志的测试用它过滤。
func withoutDebugTraceEntries(entries []applog.Entry) []applog.Entry {
	out := make([]applog.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action != "debug_trace" {
			out = append(out, entry)
		}
	}
	return out
}

// withoutEventReceived 去掉每条消息轨迹开头那条「收到消息」。
func withoutEventReceived(entries []applog.Entry) []applog.Entry {
	out := make([]applog.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Metadata["phase"] != debugTracePhaseEventReceived {
			out = append(out, entry)
		}
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

// 没有消息上下文时照样记账，只是不挂在哪条消息名下：后台建索引、定时任务、攒批
// 路由这些调用以前整条丢掉，总量比账单少一截还看不出少在哪。
func TestLLMUsageAccountingRecordsWithoutMessageContext(t *testing.T) {
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
	entries := usageEntriesFor(logs, "")
	if len(entries) != 1 || entries[0]["message_id"] != "" || entries[0]["total_tokens"] != int64(14) {
		t.Fatalf("usage entries = %#v", entries)
	}
}

// 调用点没打标签时记成 unlabeled，不能记成空 purpose，也不再去读提示词猜。
func TestLLMUsageAccountingRecordsUnlabeledPurpose(t *testing.T) {
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
	if purpose, _ := entries[0]["purpose"].(string); purpose != llmUnlabeledPurpose {
		t.Fatalf("purpose = %q, want %q: %#v", purpose, llmUnlabeledPurpose, entries[0])
	}
}

type usageMissingProvider struct{}

func (usageMissingProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "relay-model", Text: "ok"}, nil
}

// 中转不回 usage 时调用照样发生了：记一条、标 usage_missing，调用次数不能少。
func TestLLMUsageAccountingRecordsCallsWithoutReportedUsage(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	ctx := withLLMUsageContext(context.Background(), MessageEvent{MessageID: "m-relay"})
	run := runtime.withLLMUsageAccountingRun(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{})
		return "", err
	})
	if _, err := run(usageMissingProvider{}); err != nil {
		t.Fatal(err)
	}
	entries := usageEntriesFor(logs, "m-relay")
	if len(entries) != 1 || entries[0]["usage_missing"] != true || entries[0]["model"] != "relay-model" {
		t.Fatalf("usage entries = %#v", entries)
	}
}

// 已经挂过记账的 provider 再被另一条装饰链包一次，同一次调用只能记一条。
func TestLLMUsageAccountingCountsNestedWrappersOnce(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	ctx := withLLMUsageContext(context.Background(), MessageEvent{MessageID: "m-nested"})
	provider := &usageCountingProvider{}
	var inner LLMProvider
	_, _ = runtime.withLLMUsageAccountingRun(ctx, func(client LLMProvider) (string, error) {
		inner = client
		return "", nil
	})(provider)
	run := runtime.withLLMUsageAccountingRun(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{})
		return "", err
	})
	if _, err := run(inner); err != nil {
		t.Fatal(err)
	}
	if entries := usageEntriesFor(logs, "m-nested"); provider.calls != 1 || len(entries) != 1 {
		t.Fatalf("calls = %d entries = %#v", provider.calls, entries)
	}
}

// 记忆抽取和归纳走单独的 provider 选择，以前那条路上没挂记账，token 从没进过统计。
func TestMemoryProviderRecordsUsage(t *testing.T) {
	logs := &captureAppLogs{}
	provider := &usageCountingProvider{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetAppLogWriter(logs)
	ctx := withLLMUsagePurpose(withLLMUsageContext(context.Background(), MessageEvent{MessageID: "m-memory"}), "memory_extract")
	if _, err := runtime.runLLMMemoryProvider(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{})
		return "", err
	}); err != nil {
		t.Fatal(err)
	}
	entries := usageEntriesFor(logs, "m-memory")
	if len(entries) != 1 || entries[0]["purpose"] != "memory_extract" || entries[0]["total_tokens"] != int64(14) {
		t.Fatalf("usage entries = %#v", entries)
	}
}

// 生图不走文本 provider 链，得单独记；上游没报 token 时也要留下这次调用。
func TestImageUsageIsRecordedUnderTheMessage(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	ctx := withLLMUsageContext(context.Background(), MessageEvent{MessageID: "m-image"})
	cfg := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, ImageModel: "gpt-image-2"}
	runtime.recordImageUsage(ctx, cfg, &llm.ImageGenerateResponse{Images: []string{"x"}, Usage: llm.Usage{InputTokens: 50, OutputTokens: 1056}}, "image_generate", time.Second)
	runtime.recordImageUsage(ctx, cfg, &llm.ImageGenerateResponse{Images: []string{"x"}}, "image_edit", time.Second)
	entries := usageEntriesFor(logs, "m-image")
	if len(entries) != 2 {
		t.Fatalf("usage entries = %#v", entries)
	}
	if entries[0]["model"] != "gpt-image-2" || entries[0]["total_tokens"] != int64(1106) || entries[0]["purpose"] != "image_generate" {
		t.Fatalf("generate entry = %#v", entries[0])
	}
	if entries[1]["usage_missing"] != true || entries[1]["purpose"] != "image_edit" {
		t.Fatalf("edit entry = %#v", entries[1])
	}
}
