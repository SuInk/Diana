// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// embedding 服务连不上时，查询路径连着失败两次后不再去请求：生产上每条回复都为它
// 等满 3 秒超时。
func TestEmbedQueryTextStopsCallingAfterConsecutiveFailures(t *testing.T) {
	r := &Runtime{}
	calls := 0
	r.embedTexts = func(context.Context, llm.ProviderConfig, []string) ([][]float32, error) {
		calls++
		return nil, errors.New("dial timeout")
	}
	cfg := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, BaseURL: "http://embed.invalid/v1", Model: "embed-v1"}
	for range 5 {
		if _, err := r.embedQueryText(context.Background(), cfg, "跨群查询"); err == nil {
			t.Fatal("失败的 embedding 不该返回向量")
		}
	}
	if calls != semanticQueryBreakerThreshold {
		t.Fatalf("连续失败后还在请求 embedding：调用了 %d 次，应为 %d 次", calls, semanticQueryBreakerThreshold)
	}
	if _, err := r.embedQueryText(context.Background(), cfg, "跨群查询"); !errors.Is(err, errSemanticQueryBreakerOpen) {
		t.Fatalf("熔断期间应返回 errSemanticQueryBreakerOpen，实际 %v", err)
	}
	// 另一个 embedding 模型不受牵连。
	other := cfg
	other.Model = "embed-v2"
	if _, err := r.embedQueryText(context.Background(), other, "跨群查询"); errors.Is(err, errSemanticQueryBreakerOpen) {
		t.Fatal("别的模型不该被同一个熔断挡住")
	}
}

// 冷却过后放一次请求去探：成功就恢复，失败再冷却一轮，期间其余请求照旧跳过。
func TestSemanticQueryBreakerProbesAfterCooldown(t *testing.T) {
	var b semanticQueryBreaker
	now := time.Unix(1_700_000_000, 0)
	for range semanticQueryBreakerThreshold {
		b.record("k", false, now)
	}
	if b.allow("k", now.Add(semanticQueryBreakerCooldown-time.Second)) {
		t.Fatal("冷却没结束就放行了")
	}
	later := now.Add(semanticQueryBreakerCooldown + time.Second)
	if !b.allow("k", later) {
		t.Fatal("冷却结束后应放一次探测")
	}
	if b.allow("k", later) {
		t.Fatal("探测进行中不该再放第二个请求")
	}
	b.record("k", false, later)
	if b.allow("k", later.Add(time.Second)) {
		t.Fatal("探测失败后应重新冷却")
	}
	if !b.allow("k", later.Add(semanticQueryBreakerCooldown+time.Second)) {
		t.Fatal("第二轮冷却结束后应再探一次")
	}
	b.record("k", true, later)
	if !b.allow("k", later) || !b.allow("k", later) {
		t.Fatal("探测成功后应完全恢复")
	}
}

// 整轮回复被打断时取消的请求不算服务失败，不该把语义检索关掉。
func TestEmbedQueryTextIgnoresCallerCancellation(t *testing.T) {
	r := &Runtime{}
	r.embedTexts = func(ctx context.Context, _ llm.ProviderConfig, _ []string) ([][]float32, error) {
		return nil, ctx.Err()
	}
	cfg := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "embed-v1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 4 {
		if _, err := r.embedQueryText(ctx, cfg, "查询"); errors.Is(err, errSemanticQueryBreakerOpen) {
			t.Fatal("调用方取消被算成了服务失败")
		}
	}
}
