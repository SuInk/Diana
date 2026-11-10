// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 降级链里每一档失败都要落一条 llm_call_failed，不受切换日志一分钟一条的节流影响；
// 后备接上时标 recovered，今日合计里的失败次数跟着涨，成功调用次数不变。
func TestFailedAttemptsAreRecordedPerRequest(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	failing := &fixedRetryErrorProvider{err: errors.New("llm: provider request failed: 503 Service Unavailable")}
	backup := &capturingLLMProvider{reply: "备用模型回复"}
	for range 3 {
		provider, err := newProfileFailoverLLMProvider([]llm.Profile{
			{ID: "primary", Name: "主力", Group: "default", Config: llm.ProviderConfig{Model: "model-a"}},
			{ID: "backup", Group: "default", Config: llm.ProviderConfig{Model: "model-b"}},
		}, func(cfg llm.ProviderConfig) (LLMProvider, error) {
			if cfg.Model == "model-a" {
				return failing, nil
			}
			return backup, nil
		}, false, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		provider.report = runtime.reportLLMEvent
		accounted := &usageAccountingLLMProvider{runtime: runtime, state: &llmUsageState{event: MessageEvent{GroupID: "40001", MessageID: "m1"}}, provider: provider}
		if _, err := accounted.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
			t.Fatalf("backup should answer: %v", err)
		}
	}
	entries := logs.entriesSnapshot()
	if got := countAppLogAction(entries, LLMCallFailedAction); got != 3 {
		t.Fatalf("failed logs = %d (%v), want 3", got, appLogActions(entries))
	}
	for _, entry := range entries {
		if entry.Action != LLMCallFailedAction {
			continue
		}
		if entry.Metadata["model"] != "model-a" || entry.Metadata["llm_profile"] != "primary" || entry.Metadata["recovered"] != true || entry.Metadata["group_id"] != "40001" || entry.Detail == "" {
			t.Fatalf("failed entry = %#v", entry)
		}
	}
	today := runtime.llmUsageTotals().Today
	if today.FailedCalls != 3 || today.Calls != 3 {
		t.Fatalf("today = %+v, want 3 ok and 3 failed", today)
	}
}

// 调用方自己取消不算失败。
func TestCanceledCallIsNotRecordedAsFailure(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	accounted := &usageAccountingLLMProvider{runtime: runtime, state: &llmUsageState{}, provider: &fixedRetryErrorProvider{err: context.Canceled}}
	_, _ = accounted.Generate(ctx, llm.GenerateRequest{})
	if got := countAppLogAction(logs.entriesSnapshot(), LLMCallFailedAction); got != 0 {
		t.Fatalf("canceled call logged %d failures", got)
	}
}
