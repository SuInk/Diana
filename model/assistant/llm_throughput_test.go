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

// TestTokensPerSecondUsesOutputTokens 速率用输出 token，不用总量。
//
// 输入是一次性喂进去的：把它算进速率，长上下文的调用会显得「很快」，而那恰恰是
// 慢的那一类，指标的方向就反了。
func TestTokensPerSecondUsesOutputTokens(t *testing.T) {
	if got := TokensPerSecond(300, 2*time.Second); got != 150 {
		t.Fatalf("TokensPerSecond(300, 2s) = %v，应为 150", got)
	}
	// 两位小数，避免日志里出现一长串浮点尾巴。
	if got := TokensPerSecond(10, 3*time.Second); got != 3.33 {
		t.Fatalf("TokensPerSecond(10, 3s) = %v，应为 3.33", got)
	}
}

// TestTokensPerSecondGuardsAgainstGarbage 没有输出或没有耗时时给 0，不给 Inf/NaN。
//
// 耗时为 0 的调用是存在的（缓存命中、mock），除零会写出一个 JSON 都序列化不了的
// 值，把整条用量日志毁掉。
func TestTokensPerSecondGuardsAgainstGarbage(t *testing.T) {
	for _, c := range []struct {
		tokens   int64
		duration time.Duration
	}{{0, time.Second}, {100, 0}, {-5, time.Second}, {100, -time.Second}} {
		if got := TokensPerSecond(c.tokens, c.duration); got != 0 {
			t.Fatalf("TokensPerSecond(%d, %v) = %v，应为 0", c.tokens, c.duration, got)
		}
	}
}

type slowStubProvider struct {
	delay time.Duration
	usage llm.Usage
	err   error
}

func (p slowStubProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	time.Sleep(p.delay)
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "stub", Text: "hi", Usage: p.usage}, nil
}

// TestUsageAccountingRecordsDuration 装饰器要把耗时和速率写进用量日志。
//
// 计时放在装饰器里而不是各调用点：装饰器已经包住了每一次调用，新增一条调用路径
// 不会漏计。写在调用点的话，漏了不会有任何测试变红。
func TestUsageAccountingRecordsDuration(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}

	provider := &usageAccountingLLMProvider{
		runtime:  runtime,
		state:    &llmUsageState{event: event},
		provider: slowStubProvider{delay: 60 * time.Millisecond, usage: llm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
		t.Fatal(err)
	}

	entries := logs.entriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("用量日志 = %#v，应该只有一条", entries)
	}
	meta := entries[0].Metadata
	durationMS, ok := meta["duration_ms"].(int64)
	if !ok {
		t.Fatalf("duration_ms 类型不对：%#v", meta["duration_ms"])
	}
	if durationMS < 50 {
		t.Fatalf("duration_ms = %d，没有量到真实耗时", durationMS)
	}
	tps, ok := meta["tokens_per_second"].(float64)
	if !ok || tps <= 0 {
		t.Fatalf("tokens_per_second = %#v，应为正数", meta["tokens_per_second"])
	}
}

// TestUsageAccountingSkipsFailedCall 调用失败时不写用量条目。
//
// 失败没有 token 也没有可信的速率，硬记一条会把统计里的调用次数抬高、速率拉低。
func TestUsageAccountingSkipsFailedCall(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	provider := &usageAccountingLLMProvider{
		runtime:  runtime,
		state:    &llmUsageState{event: MessageEvent{Kind: EventKindGroup, MessageID: "m1"}},
		provider: slowStubProvider{err: errors.New("boom")},
	}
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{}); err == nil {
		t.Fatal("失败的调用应该把错误透传出来")
	}
	if entries := logs.entriesSnapshot(); len(entries) != 0 {
		t.Fatalf("失败的调用不该写用量条目：%#v", entries)
	}
}
