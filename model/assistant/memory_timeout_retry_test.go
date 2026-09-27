// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type timeoutCountingMemoryStore struct {
	deferringMemoryStore
	timedOut      int
	lastErrorText string
}

func (s *timeoutCountingMemoryStore) RetryTimedOutMemoryJob(_ context.Context, _ string, _ string, availableAt time.Time, lastError string) error {
	s.timedOut++
	s.availableAt = availableAt
	s.lastErrorText = lastError
	return nil
}

// 超时单独计连续次数；503 仍走不计次的延后，其他失败照常重试并清零连续计数。
func TestRetryMemoryJobTracksConsecutiveTimeouts(t *testing.T) {
	timeout := errors.New("memory summary llm: context deadline exceeded")
	store := &timeoutCountingMemoryStore{}
	before := time.Now()
	if err := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 1}, "w", timeout); err != nil {
		t.Fatal(err)
	}
	if store.timedOut != 1 || store.retried != 0 || store.deferred != 0 || store.lastErrorText != timeout.Error() {
		t.Fatalf("timeout retry: %+v", store)
	}
	if store.availableAt.Before(before.Add(memoryRetryDelay(1))) {
		t.Fatalf("timeout retry below the limit should still back off: %v", store.availableAt.Sub(before))
	}

	// 这一次就到上限：不再等退避，马上放回去让下一轮领取直接放弃。
	store = &timeoutCountingMemoryStore{}
	before = time.Now()
	if err := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 3, ConsecutiveTimeouts: memoryMaxConsecutiveTimeouts - 1}, "w", timeout); err != nil {
		t.Fatal(err)
	}
	if store.timedOut != 1 || store.availableAt.After(before.Add(time.Second)) {
		t.Fatalf("last timeout should requeue immediately: timedOut=%d wait=%v", store.timedOut, store.availableAt.Sub(before))
	}

	store = &timeoutCountingMemoryStore{}
	if err := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 2}, "w", errors.New("llm: openai-compatible request failed: 503 Service Unavailable")); err != nil {
		t.Fatal(err)
	}
	if store.deferred != 1 || store.timedOut != 0 {
		t.Fatalf("outage: %+v", store)
	}

	store = &timeoutCountingMemoryStore{}
	if err := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 2}, "w", errors.New("decode memory gate response: unexpected EOF")); err != nil {
		t.Fatal(err)
	}
	if store.retried != 1 || store.timedOut != 0 {
		t.Fatalf("other failure: %+v", store)
	}
}

func TestMemoryJobAbandonReason(t *testing.T) {
	if _, abandon := memoryJobAbandonReason(MemoryJob{Attempts: 3, ConsecutiveTimeouts: memoryMaxConsecutiveTimeouts - 1}); abandon {
		t.Fatal("below the timeout limit the job should still run")
	}
	reason, abandon := memoryJobAbandonReason(MemoryJob{Attempts: 4, ConsecutiveTimeouts: memoryMaxConsecutiveTimeouts})
	if !abandon || !strings.Contains(reason, "超时") {
		t.Fatalf("timeout limit reason=%q abandon=%v", reason, abandon)
	}
	reason, abandon = memoryJobAbandonReason(MemoryJob{Attempts: memoryMaxAttempts + 1})
	if !abandon || !strings.Contains(reason, "次数用完") {
		t.Fatalf("attempts exhausted reason=%q abandon=%v", reason, abandon)
	}
}

// timedOutMemoryStore 交出一个已经连续超时到上限的摘要任务，并记下完成调用。
type timedOutMemoryStore struct {
	*testStructuredMemoryStore
	claimed   bool
	completed []string
}

func (s *timedOutMemoryStore) ClaimNextMemoryJob(context.Context, string, time.Time) (MemoryJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return MemoryJob{}, false, nil
	}
	s.claimed = true
	return MemoryJob{
		ID: "job-timeout", Attempts: 4, ConsecutiveTimeouts: memoryMaxConsecutiveTimeouts,
		LastError: "memory summary llm: context deadline exceeded",
		Payload:   MemoryJobPayload{Kind: MemoryJobSummary, Session: "group:30001"},
	}, true, nil
}

func (s *timedOutMemoryStore) CompleteMemoryJob(_ context.Context, id string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, id)
	return nil
}

// 放弃时日志详情里要有最后一次的报错，不然事后只知道「放弃了」不知道为什么。
func TestAbandonedMemoryJobLogsLastError(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		t.Error("an abandoned job must not call the model")
		return nil, errors.New("unexpected")
	})
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	store := &timedOutMemoryStore{testStructuredMemoryStore: &testStructuredMemoryStore{}}

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
		if entry.Action != "memory_job_abandoned" {
			continue
		}
		if entry.Detail != "memory summary llm: context deadline exceeded" || entry.Metadata["timeouts"] != memoryMaxConsecutiveTimeouts || !strings.Contains(entry.Message, "超时") {
			t.Fatalf("abandoned entry = %#v", entry)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.completed) != 1 || store.completed[0] != "job-timeout" {
		t.Fatalf("completed = %v", store.completed)
	}
}

// 重试时摘要输入整体缩小：不再要卷叠，旧摘要只带最近的几条。
func TestSummaryMemoryJobRetryDropsRollupAndOldSummaries(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	memory := &testStructuredMemoryStore{}
	total := memorySummaryRollupSize + 8
	for index := 0; index < total; index++ {
		memory.items = append(memory.items, StructuredMemoryItem{
			ID: fmt.Sprintf("s%02d", index), Key: fmt.Sprintf("summary.2025-01-%02d.topic", index+1), Kind: MemoryKindSummary,
			Content: fmt.Sprintf("第%02d天摘要", index+1), Importance: 0.8, Status: MemoryStatusActive,
			SourceEventTime: base.AddDate(0, 0, index),
		})
	}
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "a", MessageID: "new", Time: base.AddDate(0, 1, 0).Unix(),
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "新的话题"}}},
	}
	run := func(attempts int) summaryPromptInput {
		t.Helper()
		provider := &capturingLLMProvider{reply: `{"memories":[]}`}
		runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		if err := runtime.processSummaryMemoryJob(context.Background(), memory, MemoryJob{
			ID: "summary-job", Attempts: attempts, Payload: MemoryJobPayload{Kind: MemoryJobSummary, Session: "group:123", Events: []MessageEvent{event}},
		}); err != nil {
			t.Fatal(err)
		}
		return decodeSummaryPromptInput(t, provider.request.Messages[len(provider.request.Messages)-1].Content)
	}

	first := run(1)
	if first.Rollup == nil || len(first.Rollup.Sources) != memorySummaryRollupSize {
		t.Fatalf("first attempt should carry the rollup: %#v", first.Rollup)
	}
	// 卷叠源已经在 rollup 里，existing_summaries 不再重复一遍。
	if len(first.Existing) != total-memorySummaryRollupSize {
		t.Fatalf("first attempt existing = %d, want the %d summaries outside the rollup", len(first.Existing), total-memorySummaryRollupSize)
	}
	for _, item := range first.Existing {
		for _, source := range first.Rollup.Sources {
			if item.Key == source.Key {
				t.Fatalf("rollup source %s duplicated in existing_summaries", item.Key)
			}
		}
	}

	retry := run(2)
	if retry.Rollup != nil {
		t.Fatalf("retry should skip the rollup: %#v", retry.Rollup)
	}
	if len(retry.Existing) != memorySummaryRetryExistingMax {
		t.Fatalf("retry existing = %d, want %d", len(retry.Existing), memorySummaryRetryExistingMax)
	}
	oldest := fmt.Sprintf("summary.2025-01-%02d.topic", total-memorySummaryRetryExistingMax+1)
	for _, item := range retry.Existing {
		if item.Key < oldest {
			t.Fatalf("retry kept an old summary %s, want only the latest %d", item.Key, memorySummaryRetryExistingMax)
		}
	}
}

type summaryPromptInput struct {
	Existing []memoryGateMemory `json:"existing_summaries"`
	Rollup   *struct {
		Sources []memoryGateMemory `json:"source_summaries"`
	} `json:"rollup"`
}

func decodeSummaryPromptInput(t *testing.T, prompt string) summaryPromptInput {
	t.Helper()
	start := strings.Index(prompt, "{")
	if start < 0 {
		t.Fatalf("summary prompt has no JSON: %s", prompt)
	}
	var input summaryPromptInput
	if err := json.Unmarshal([]byte(prompt[start:]), &input); err != nil {
		t.Fatalf("decode summary prompt: %v", err)
	}
	return input
}

// 带卷叠的摘要超时后，这个会话在冷却期内首轮也不再带卷叠；冷却过后重试卷叠，写成后清掉冷却。
func TestMemoryRollupBackoffAfterTimeout(t *testing.T) {
	runtime := &Runtime{}
	now := time.Now()
	if !runtime.memoryRollupAllowed("group:1", now) {
		t.Fatal("rollup should be allowed before any timeout")
	}
	runtime.noteMemoryRollupTimeout("group:1", now)
	if runtime.memoryRollupAllowed("group:1", now.Add(time.Minute)) {
		t.Fatal("rollup should be skipped right after a timeout")
	}
	if !runtime.memoryRollupAllowed("group:2", now) {
		t.Fatal("backoff must not leak to other sessions")
	}
	if !runtime.memoryRollupAllowed("group:1", now.Add(memoryRollupCooldownBase)) {
		t.Fatal("rollup should be retried after the cooldown")
	}
	runtime.noteMemoryRollupTimeout("group:1", now)
	if runtime.memoryRollupAllowed("group:1", now.Add(memoryRollupCooldownBase)) {
		t.Fatal("second timeout should double the cooldown")
	}
	if got := memoryRollupCooldown(20); got != memoryRollupCooldownMax {
		t.Fatalf("cooldown cap = %v", got)
	}
	runtime.clearMemoryRollupBackoff("group:1")
	if !runtime.memoryRollupAllowed("group:1", now) {
		t.Fatal("a written rollup should clear the backoff")
	}
}
