// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 配置档切换以前这条路径连终端日志都没有：换到了备用模型，界面上哪里都看不出来。
// 现在切一次记一条；上游挂着的时候每次调用都会切，一分钟只记一条。
func TestProfileFailoverIsLoggedAndThrottled(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	failing := &fixedRetryErrorProvider{err: errors.New("llm: provider request failed: 503 Service Unavailable")}
	backup := &capturingLLMProvider{reply: "备用模型回复"}
	build := func() *profileFailoverLLMProvider {
		provider, err := newProfileFailoverLLMProvider([]llm.Profile{
			{ID: "primary", Group: "default", Config: llm.ProviderConfig{Model: "model-a"}},
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
		return provider
	}
	for range 3 {
		if _, err := build().Generate(context.Background(), llm.GenerateRequest{}); err != nil {
			t.Fatalf("backup should answer: %v", err)
		}
	}
	entries := logs.entriesSnapshot()
	if countAppLogAction(entries, "llm_failover") != 1 {
		t.Fatalf("failover logs = %v, want exactly one within a minute", appLogActions(entries))
	}
	entry := entries[0]
	if entry.Metadata["from"] != "primary" || entry.Metadata["to"] != "backup" || !strings.Contains(entry.Detail, "503") ||
		!strings.Contains(entry.Message, "primary") || !strings.Contains(entry.Message, "backup") {
		t.Fatalf("failover entry = %#v", entry)
	}
}

// 上下文超限收缩重发要留痕：回复里像是少了前面的聊天记录，原因就在这里。
func TestContextShrinkIsReported(t *testing.T) {
	var events []llmEvent
	ctx := withLLMEventReporter(context.Background(), func(event llmEvent) { events = append(events, event) })
	provider := &contextOverflowProvider{limit: 20_000}
	if _, err := generateWithTransientRetryPolicy(ctx, provider,
		llm.GenerateRequest{Model: "model-x", Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}},
		true, 0, 0, 0); err != nil {
		t.Fatalf("shrunk retry should succeed: %v", err)
	}
	if len(events) == 0 || events[0].Kind != llmEventContextShrink || events[0].Model != "model-x" || events[0].MaxContextTokens <= 0 {
		t.Fatalf("events = %#v", events)
	}
}

// 后台任务失败的节流按 key 分开：同一件事一分钟一条，不同的事互不影响。
func TestBackgroundFailureThrottlesPerKey(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	for range 3 {
		runtime.recordBackgroundFailure("memory_job_failed", "长期记忆提取失败，稍后自动重试", "", errors.New("timeout"), nil)
	}
	runtime.recordBackgroundFailure("semantic_index_failed", "语义检索的向量生成失败", "", errors.New("401"), nil)
	entries := logs.entriesSnapshot()
	if countAppLogAction(entries, "memory_job_failed") != 1 || countAppLogAction(entries, "semantic_index_failed") != 1 {
		t.Fatalf("entries = %v", appLogActions(entries))
	}
	if entries[0].Level != "error" || entries[0].Detail != "timeout" {
		t.Fatalf("entry = %#v", entries[0])
	}
	if !runtime.backgroundLogThrottle.allow("memory_job_failed", time.Now().Add(2*time.Minute)) {
		t.Fatal("throttle must reopen after the interval")
	}
}

// abandoningMemoryStore 交出一个重试次数已经用完的任务，入队一律失败。
type abandoningMemoryStore struct {
	*testStructuredMemoryStore
	claimed bool
}

func (s *abandoningMemoryStore) ClaimNextMemoryJob(context.Context, string, time.Time) (MemoryJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return MemoryJob{}, false, nil
	}
	s.claimed = true
	return MemoryJob{ID: "job-1", Attempts: memoryMaxAttempts + 1, Payload: MemoryJobPayload{Kind: MemoryJobEvent, Session: "group:20001"}}, true, nil
}

func (s *abandoningMemoryStore) EnqueueMemoryJob(context.Context, MemoryJobPayload) (string, bool, error) {
	return "", false, errors.New("database is locked")
}

// 记忆任务放弃、排不进队列，以前只打到终端，界面上只看得到「机器人好像没记住」。
func TestMemoryJobFailuresAreLogged(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	store := &abandoningMemoryStore{testStructuredMemoryStore: &testStructuredMemoryStore{}}
	runtime.SetStructuredMemoryStore(store)

	runtime.enqueueContextSummary("group:20001", []MessageEvent{{Kind: EventKindGroup, GroupID: "20001", UserID: "u", MessageID: "m"}})
	if !hasAppLogAction(logs.entriesSnapshot(), "memory_enqueue_failed") {
		t.Fatalf("enqueue failure not logged: %v", appLogActions(logs.entriesSnapshot()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.runMemoryWorker(ctx, "owner", store)
	}()
	waitForCondition(t, 3*time.Second, func() bool { return hasAppLogAction(logs.entriesSnapshot(), "memory_job_abandoned") })
	cancel()
	<-done
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action == "memory_job_abandoned" && (entry.Metadata["session"] != "group:20001" || entry.Metadata["attempts"] != memoryMaxAttempts) {
			t.Fatalf("abandoned entry = %#v", entry)
		}
	}
}
