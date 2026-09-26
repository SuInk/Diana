// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
)

type deferringMemoryStore struct {
	testStructuredMemoryStore
	retried, deferred int
	availableAt       time.Time
}

func (s *deferringMemoryStore) RetryMemoryJob(_ context.Context, _ string, _ string, availableAt time.Time, _ string) error {
	s.retried++
	s.availableAt = availableAt
	return nil
}

func (s *deferringMemoryStore) DeferMemoryJob(_ context.Context, _ string, _ string, availableAt time.Time, _ string, _ time.Time) error {
	s.deferred++
	s.availableAt = availableAt
	return nil
}

// 线上 sub2api 连续 5 小时 503，记忆任务 47 分钟内就把 8 次额度耗光被放弃。网关
// 整体不可用要走不计次的延后；超时仍然计次，摘要缩窗靠的就是次数。
func TestRetryMemoryJobDefersUpstreamOutageWithoutSpendingAttempts(t *testing.T) {
	outages := []error{
		errors.New(`memory gate llm: 配置档「默认配置」(openai_compatible · gpt-5.5) 调用失败：llm: provider request failed: llm: openai-compatible request failed: 503 Service Unavailable: type=api_error; message=Service temporarily unavailable`),
		errors.New(`llm: openai-compatible request failed: 429 Too Many Requests: code=gateway_concurrency_limit; type=rate_limit_error; message=Concurrency limit exceeded for account, please retry later`),
		errors.New(`llm: openai-compatible request failed: 502 Bad Gateway: type=upstream_error; message=Upstream service temporarily unavailable`),
	}
	for _, err := range outages {
		store := &deferringMemoryStore{}
		before := time.Now()
		if stateErr := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 3}, "w", err); stateErr != nil {
			t.Fatal(stateErr)
		}
		if store.deferred != 1 || store.retried != 0 {
			t.Fatalf("%v: deferred=%d retried=%d", err, store.deferred, store.retried)
		}
		if store.availableAt.Before(before.Add(memoryOutageRetryDelay)) {
			t.Fatalf("outage retry too soon: %v", store.availableAt.Sub(before))
		}
	}

	for _, err := range []error{
		errors.New("memory summary llm: context deadline exceeded"),
		errors.New("llm: openai-compatible event stream output is empty"),
		errors.New("400 Bad Request: code=invalid_request_error"),
	} {
		store := &deferringMemoryStore{}
		if stateErr := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 3}, "w", err); stateErr != nil {
			t.Fatal(stateErr)
		}
		if store.retried != 1 || store.deferred != 0 {
			t.Fatalf("%v: deferred=%d retried=%d", err, store.deferred, store.retried)
		}
	}
}

// 存储没实现 DeferMemoryJob 时照旧计次重试，不能因为认出了故障就把任务丢下。
func TestRetryMemoryJobFallsBackWithoutDeferrer(t *testing.T) {
	store := &testStructuredMemoryStore{}
	err := errors.New("503 Service Unavailable")
	if stateErr := retryMemoryJob(context.Background(), store, MemoryJob{ID: "j", Attempts: 1}, "w", err); stateErr != nil {
		t.Fatal(stateErr)
	}
}
