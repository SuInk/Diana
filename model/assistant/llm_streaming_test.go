// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// stubStreamer 既能流式也能非流式，用来验证退回逻辑。
type stubStreamer struct {
	events      []llm.ChatEvent
	streamErr   error
	generateOut string
	generateErr error
	gapPerDelta time.Duration
	generated   bool
}

func (s *stubStreamer) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	s.generated = true
	if s.generateErr != nil {
		return nil, s.generateErr
	}
	return &llm.GenerateResponse{Text: s.generateOut, Usage: llm.Usage{OutputTokens: 7}}, nil
}

func (s *stubStreamer) Stream(context.Context, llm.GenerateRequest) (<-chan llm.ChatEvent, error) {
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	out := make(chan llm.ChatEvent, len(s.events)+1)
	go func() {
		defer close(out)
		for _, event := range s.events {
			if s.gapPerDelta > 0 && event.Type == llm.ChatEventTextDelta {
				time.Sleep(s.gapPerDelta)
			}
			out <- event
		}
	}()
	return out, nil
}

func textDelta(text string) llm.ChatEvent {
	return llm.ChatEvent{Type: llm.ChatEventTextDelta, Text: text}
}

// TestStreamingAccumulatesCompleteResponse 流式跑通后交出去的必须是完整响应。
//
// 上面四层装饰器（身份脱敏尤其）是按「拿到完整文本」写的：脱敏要对全文做别名
// 还原，别名会跨 chunk 边界。这里交出半截文本，那一层就会切坏。
func TestStreamingAccumulatesCompleteResponse(t *testing.T) {
	stub := &stubStreamer{events: []llm.ChatEvent{
		textDelta("你好"), textDelta("，"), textDelta("世界"),
		{Type: llm.ChatEventUsage, Usage: &llm.Usage{InputTokens: 5, OutputTokens: 3}},
		{Type: llm.ChatEventDone},
	}}
	response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "你好，世界" {
		t.Fatalf("攒出来的文本 = %q", response.Text)
	}
	if response.Usage.OutputTokens != 3 {
		t.Fatalf("用量没带过来：%+v", response.Usage)
	}
	if stub.generated {
		t.Fatal("流式已经成功，不该再走一次非流式")
	}
}

// TestStreamingFallsBackOnError 起流失败、流中报错，都要退回非流式。
//
// 流式只为一个诊断指标，不该决定回复发不发得出去。
func TestStreamingFallsBackOnError(t *testing.T) {
	t.Run("起流就失败", func(t *testing.T) {
		stub := &stubStreamer{streamErr: errors.New("no stream"), generateOut: "兜底回复"}
		response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
		if err != nil || response.Text != "兜底回复" {
			t.Fatalf("没有退回非流式：%v %#v", err, response)
		}
	})
	t.Run("流中报错", func(t *testing.T) {
		stub := &stubStreamer{
			events:      []llm.ChatEvent{textDelta("半"), {Type: llm.ChatEventError, Error: "boom"}},
			generateOut: "兜底回复",
		}
		response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if response.Text != "兜底回复" {
			t.Fatalf("流中报错后没有退回非流式，拿到半截文本 %q", response.Text)
		}
	})
}

func TestStreamingFallsBackOnEmptyOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []llm.ChatEvent
	}{
		{"empty", []llm.ChatEvent{{Type: llm.ChatEventDone}}},
		{"reasoning_only", []llm.ChatEvent{{Type: llm.ChatEventReasoning, Reasoning: "calling a tool"}, {Type: llm.ChatEventUsage, Usage: &llm.Usage{OutputTokens: 32}}, {Type: llm.ChatEventDone}}},
		{"whitespace", []llm.ChatEvent{textDelta(" \n\t"), {Type: llm.ChatEventDone}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubStreamer{events: tc.events, generateOut: "fallback"}
			response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
			if err != nil || !stub.generated || response == nil || response.Text != "fallback" {
				t.Fatalf("empty stream did not fall back: response=%+v error=%v generated=%v", response, err, stub.generated)
			}
		})
	}
	stub := &stubStreamer{events: []llm.ChatEvent{{Type: llm.ChatEventDone}}, generateErr: errors.New("provider output is empty")}
	if _, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{}); !errors.Is(err, stub.generateErr) {
		t.Fatalf("fallback failure was swallowed: %v", err)
	}
}

func TestStreamingToolOnlyResponseDoesNotRetry(t *testing.T) {
	call := llm.ToolCall{ID: "call_1", Name: "agent_finalize", Arguments: map[string]any{"silent": true}}
	stub := &stubStreamer{events: []llm.ChatEvent{{Type: llm.ChatEventToolCall, ToolCall: &call}, {Type: llm.ChatEventDone}}}
	response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil || stub.generated || response == nil || len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != call.ID {
		t.Fatalf("tool-only response should succeed without retry: response=%+v error=%v generated=%v", response, err, stub.generated)
	}
}

// TestStreamingSkipsNonStreamingProvider 不支持流式的 provider 原样走 Generate。
func TestStreamingSkipsNonStreamingProvider(t *testing.T) {
	plain := slowStubProvider{usage: llm.Usage{OutputTokens: 3}}
	response, err := (&streamingLLMProvider{provider: plain}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil || response == nil {
		t.Fatalf("不支持流式的 provider 应该照常返回：%v", err)
	}
}

// TestTTFTNeedsMoreThanOneDelta 是这次最容易被忽略的一条。
//
// 只提供完整文本的旧适配器仍可能发送单个 delta，不能把总耗时当成首 token 时间。
func TestTTFTNeedsMoreThanOneDelta(t *testing.T) {
	started := time.Now()

	single := &ttftCollector{}
	single.observeDelta(started.Add(50 * time.Millisecond))
	if got := single.ttft(started); got != 0 {
		t.Fatalf("只有一条增量时报出了 TTFT = %v（底层已退化成非流式）", got)
	}

	multi := &ttftCollector{}
	multi.observeDelta(started.Add(30 * time.Millisecond))
	multi.observeDelta(started.Add(80 * time.Millisecond))
	if got := multi.ttft(started); got != 30*time.Millisecond {
		t.Fatalf("多条增量时的 TTFT = %v，应为首条的 30ms", got)
	}
}

// TestTTFTReachesUsageLog 端到端：开了流式，用量日志里要有 ttft_ms。
func TestTTFTReachesUsageLog(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	stub := &stubStreamer{
		gapPerDelta: 20 * time.Millisecond,
		events: []llm.ChatEvent{
			textDelta("一"), textDelta("二"), textDelta("三"),
			{Type: llm.ChatEventUsage, Usage: &llm.Usage{InputTokens: 4, OutputTokens: 3}},
			{Type: llm.ChatEventDone},
		},
	}
	provider := &usageAccountingLLMProvider{
		runtime:  runtime,
		state:    &llmUsageState{event: MessageEvent{Kind: EventKindGroup, MessageID: "m1"}},
		provider: &streamingLLMProvider{provider: stub},
	}
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	entries := logs.entriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("用量日志 = %#v", entries)
	}
	ttft, ok := entries[0].Metadata["ttft_ms"].(int64)
	if !ok || ttft <= 0 {
		t.Fatalf("ttft_ms = %#v，应为正数", entries[0].Metadata["ttft_ms"])
	}
	// TTFT 必须明显小于整次调用的耗时，否则说明量的其实是总时间。
	duration, _ := entries[0].Metadata["duration_ms"].(int64)
	if ttft >= duration {
		t.Fatalf("ttft_ms=%d 不小于 duration_ms=%d，量到的不是首 token", ttft, duration)
	}
}

// TestUsageLogOmitsTTFTWhenNotStreaming 没开流式时不写这个键。
//
// 写一个 0 进去，聚合那边分不清「没开流式」和「首 token 真的是 0 毫秒」，
// 均值会被稀释成假的。
func TestUsageLogOmitsTTFTWhenNotStreaming(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	provider := &usageAccountingLLMProvider{
		runtime:  runtime,
		state:    &llmUsageState{event: MessageEvent{Kind: EventKindGroup, MessageID: "m1"}},
		provider: slowStubProvider{delay: 5 * time.Millisecond, usage: llm.Usage{OutputTokens: 9}},
	}
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	entries := logs.entriesSnapshot()
	if _, present := entries[0].Metadata["ttft_ms"]; present {
		t.Fatalf("没开流式却写了 ttft_ms：%#v", entries[0].Metadata)
	}
}

func TestStreamingDefaultsOnAndPreservesExplicitOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting *bool
		want    bool
	}{
		{"missing", nil, true}, {"on", boolPointer(true), true}, {"off", boolPointer(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BotConfig{LLMStreamingEnabled: tc.setting}.WithDefaults()
			if boolValue(cfg.LLMStreamingEnabled, false) != tc.want {
				t.Fatalf("default not applied: %+v", cfg.LLMStreamingEnabled)
			}
			runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			run := func(provider LLMProvider) (string, error) {
				_, streaming := provider.(*streamingLLMProvider)
				if streaming != tc.want {
					t.Fatalf("streaming=%v want=%v", streaming, tc.want)
				}
				return "", nil
			}
			if _, err := runtime.withLLMStreamingRun(context.Background(), run)(slowStubProvider{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamingPreservesPrivateContinuationState(t *testing.T) {
	reasoning := "private reasoning"
	meta := &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "mimo", ReasoningContent: &reasoning, ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}
	stub := &stubStreamer{events: []llm.ChatEvent{{Type: llm.ChatEventReasoning, Reasoning: reasoning}, {Type: llm.ChatEventToolCall, ToolCall: &llm.ToolCall{ID: "call", Name: "lookup"}}, {Type: llm.ChatEventDone, Response: meta}}}
	response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil || response.Text != "" || response.Model != "mimo" || response.ReasoningContent == nil || *response.ReasoningContent != reasoning || len(response.ResponsesOutput) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), reasoning) || strings.Contains(string(encoded), "opaque") {
		t.Fatal("continuation leaked into public JSON")
	}
}

func TestStreamingRetriesTruncatedVisibleOutput(t *testing.T) {
	stub := &stubStreamer{events: []llm.ChatEvent{textDelta("partial")}, generateOut: "complete"}
	response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil || response.Text != "complete" || !stub.generated {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
