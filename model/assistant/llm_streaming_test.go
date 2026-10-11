// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestStreamingOnlyFallsBackForExplicitUnsupported(t *testing.T) {
	for _, tc := range []struct {
		message  string
		fallback bool
	}{
		{"streaming is not supported", true},
		{"HTTP 401: streaming is not supported", false},
		{"HTTP 429: streaming is not supported", false},
		{"HTTP 500", false}, {"no stream", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			stub := &stubStreamer{streamErr: errors.New(tc.message), generateOut: "fallback"}
			response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
			if stub.generated != tc.fallback || (err == nil) != tc.fallback || (response != nil) != tc.fallback {
				t.Fatalf("response=%+v error=%v generated=%v", response, err, stub.generated)
			}
		})
	}
}

func TestStreamingEmptyOutputDoesNotRestartNonStreaming(t *testing.T) {
	for _, events := range [][]llm.ChatEvent{
		{{Type: llm.ChatEventDone}},
		{{Type: llm.ChatEventReasoning, Reasoning: "thinking"}, {Type: llm.ChatEventDone}},
		{textDelta(" \n\t"), {Type: llm.ChatEventDone}},
	} {
		stub := &stubStreamer{events: events, generateOut: "fallback"}
		response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
		if !errors.Is(err, llm.ErrCompletionEmpty) || response != nil || stub.generated {
			t.Fatalf("response=%+v err=%v generated=%v", response, err, stub.generated)
		}
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

func TestStreamingTruncationDoesNotRestartNonStreaming(t *testing.T) {
	stub := &stubStreamer{events: []llm.ChatEvent{textDelta("partial")}, generateOut: "complete"}
	response, err := (&streamingLLMProvider{provider: stub}).Generate(context.Background(), llm.GenerateRequest{})
	if err == nil || response != nil || stub.generated {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

type recordingDeltaObserver struct{ texts []string }

func (o *recordingDeltaObserver) ObserveTextDelta(_ context.Context, text string) bool {
	o.texts = append(o.texts, text)
	return true
}

func TestCommittedStreamStopsEveryOuterRetry(t *testing.T) {
	for _, cause := range []error{errors.New("HTTP 500"), errors.New("HTTP 529"), llm.ErrUnverifiedRejection, errors.New("maximum context length exceeded"), context.DeadlineExceeded} {
		primary := &streamRejectionRegistryAdapter{events: []llm.ChatEvent{textDelta("partial"), {Type: llm.ChatEventError, Error: cause.Error(), ErrorCause: cause}}}
		backup := &streamRejectionRegistryAdapter{events: streamedReplyEvents("backup")}
		provider := streamRejectionProvider(t, primary, backup)
		observer := &recordingDeltaObserver{}
		ctx := withTextDeltaObserver(context.Background(), observer)
		response, err := generateWithTransientRetryPolicy(ctx, &streamingLLMProvider{provider: provider}, llm.GenerateRequest{}, true, 0, 3, 0)
		if response != nil || !errors.Is(err, errLLMOutputCommitted) || !errors.Is(err, cause) || shouldFailoverLLMError(err) || shouldRetryTransientLLMError(err) || !llmUnusableForRewrite(err) {
			t.Fatalf("cause=%v response=%+v err=%v", cause, response, err)
		}
		if primary.streamCalls != 1 || primary.generateCalls != 0 || backup.streamCalls != 0 || backup.generateCalls != 0 || len(observer.texts) != 1 {
			t.Fatalf("requests primary=%+v backup=%+v observer=%+v", primary, backup, observer)
		}
	}
}

func TestProfileStreamingUsesCandidateLocalBudget(t *testing.T) {
	profiles := []llm.Profile{{ID: "primary", Config: llm.ProviderConfig{Model: "primary"}}, {ID: "backup", Config: llm.ProviderConfig{Model: "backup"}}}
	primary := &stubStreamer{streamErr: errors.New("HTTP 401")}
	backup := &stubStreamer{events: streamedReplyEvents("backup")}
	provider, err := newProfileFailoverLLMProvider(profiles, func(cfg llm.ProviderConfig) (LLMProvider, error) {
		if cfg.Model == "primary" {
			return primary, nil
		}
		return backup, nil
	}, true, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil || response.Text != "backup" || primary.generated || backup.generated {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestStreamCandidateAttemptsResetForEachGenerate(t *testing.T) {
	primary := &streamRejectionRegistryAdapter{events: midStreamLimitedEvents()}
	backup := &streamRejectionRegistryAdapter{events: midStreamLimitedEvents()}
	provider := streamRejectionProvider(t, primary, backup)
	for range 2 {
		if _, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{}); err == nil {
			t.Fatal("expected error")
		}
	}
	if primary.streamCalls != 2 || backup.streamCalls != 2 || primary.generateCalls != 0 || backup.generateCalls != 0 {
		t.Fatalf("primary=%+v backup=%+v", primary, backup)
	}
}

type failingDraftChannel struct{ err error }

func (c failingDraftChannel) SendTextDraft(context.Context, OutgoingMessage, int64) error {
	return c.err
}

func TestDraftCommitFeedbackControlsFailover(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		draft     *telegramReplyDraft
		committed bool
	}{
		{"nil", "partial", nil, false},
		{"disabled", "partial", &telegramReplyDraft{channel: failingDraftChannel{}, disabled: true}, false},
		{"blank", " \n", &telegramReplyDraft{channel: failingDraftChannel{}}, false},
		{"metadata", replySingleMarker[:5], &telegramReplyDraft{channel: failingDraftChannel{}}, false},
		{"rejected", "partial", &telegramReplyDraft{channel: failingDraftChannel{err: &telegramAPIError{Code: 400}}}, false},
		{"sent", "partial", &telegramReplyDraft{channel: failingDraftChannel{}}, true},
		{"uncertain", "partial", &telegramReplyDraft{channel: failingDraftChannel{err: errors.New("connection reset")}}, true},
		{"server-error", "partial", &telegramReplyDraft{channel: failingDraftChannel{err: &telegramAPIError{Code: 500}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := &streamRejectionRegistryAdapter{events: []llm.ChatEvent{textDelta(tc.text), {Type: llm.ChatEventError, Error: "HTTP 401"}}}
			backup := &streamRejectionRegistryAdapter{events: streamedReplyEvents("backup")}
			ctx := withTextDeltaObserver(context.Background(), tc.draft)
			response, err := (&streamingLLMProvider{provider: streamRejectionProvider(t, primary, backup)}).Generate(ctx, llm.GenerateRequest{})
			if tc.committed {
				if !errors.Is(err, llm.ErrOutputCommitted) || backup.streamCalls != 0 {
					t.Fatalf("response=%+v err=%v backup=%d", response, err, backup.streamCalls)
				}
			} else if err != nil || response == nil || response.Text != "backup" || backup.streamCalls != 1 {
				t.Fatalf("response=%+v err=%v backup=%d", response, err, backup.streamCalls)
			}
		})
	}
}

func TestRegistryNativeStreamEmptyRetriesAndFailsOver(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	profiles := []llm.Profile{{ID: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIFormat: llm.APIFormatChatCompletions, BaseURL: server.URL, APIKey: "test", Model: "primary"}}, {ID: "backup", Config: llm.ProviderConfig{Model: "backup"}}}
	registry, _, err := llm.NewProviderRegistryFromProfiles(llm.ProfileSet{Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	backup := &streamRejectionRegistryAdapter{events: streamedReplyEvents("backup")}
	if err := registry.RegisterProvider(llm.ProviderDefinition{ID: "backup", Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, backup); err != nil {
		t.Fatal(err)
	}
	provider, err := newRegistryFailoverLLMProvider(registry, profiles, true, false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}}})
	if err != nil || response == nil || response.Text != "backup" || calls.Load() != int32(llmTransientMaxRetries+1) || backup.streamCalls != 1 {
		t.Fatalf("response=%+v err=%v calls=%d backup=%d", response, err, calls.Load(), backup.streamCalls)
	}
}

func TestRegistryCompatibilityStreamDecisionFailsOver(t *testing.T) {
	profiles := []llm.Profile{{ID: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderTypeSafe, APIKey: "test", Model: "jev-latest"}}, {ID: "backup", Config: llm.ProviderConfig{Model: "backup"}}}
	registry, selection, err := llm.NewProviderRegistryFromProfiles(llm.ProfileSet{Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&streamingLLMProvider{provider: llm.RegistryClient{Registry: registry, Selection: selection}}).Generate(context.Background(), llm.GenerateRequest{})
	if !errors.Is(err, llm.ErrDecisionRequired) {
		t.Fatalf("cause lost: %v", err)
	}
	backup := &streamRejectionRegistryAdapter{events: streamedReplyEvents("backup")}
	if err := registry.RegisterProvider(llm.ProviderDefinition{ID: "backup", Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, backup); err != nil {
		t.Fatal(err)
	}
	provider, err := newRegistryFailoverLLMProvider(registry, profiles, true, false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), llm.GenerateRequest{})
	if err != nil || response == nil || response.Text != "backup" || backup.streamCalls != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRegistryActivityAwareStreamSurvivesAttemptDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 6 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	registry, selection, err := llm.NewProviderRegistryFromProfiles(llm.NewProfileSet(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIFormat: llm.APIFormatChatCompletions, BaseURL: server.URL, APIKey: "test", Model: "test", Timeout: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	client := llm.RegistryClient{Registry: registry, Selection: selection}
	if !client.ManagesAttemptTimeout() {
		t.Fatal("registry lost timeout ownership")
	}
	provider := &streamingLLMProvider{provider: client}
	response, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}}}, false, 50*time.Millisecond, 0, 0)
	if err != nil || response == nil || response.Text != "xxxxxx" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err = generateWithTransientRetryPolicy(ctx, provider, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}}}, false, 50*time.Millisecond, 0, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline lost: %v", err)
	}
}

func TestModelRequestTimeoutHTTPHeartbeatFailover(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("registry=%v/stream=%v", registered, streaming), func(t *testing.T) {
				var mu sync.Mutex
				calls := map[string]int{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Model  string `json:"model"`
						Stream bool   `json:"stream"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					calls[request.Model]++
					mu.Unlock()
					if request.Stream {
						w.Header().Set("Content-Type", "text/event-stream")
						if request.Model == "third" {
							fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
							return
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
						if request.Model == "third" {
							fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
							return
						}
					}
					for {
						if request.Stream {
							fmt.Fprint(w, ": heartbeat\n\n")
						} else {
							fmt.Fprint(w, " ")
						}
						w.(http.Flusher).Flush()
						select {
						case <-r.Context().Done():
							return
						case <-time.After(5 * time.Millisecond):
						}
					}
				}))
				defer server.Close()
				set := llm.ProfileSet{}
				for _, name := range []string{"first", "second", "third"} {
					set.Profiles = append(set.Profiles, llm.Profile{ID: name, Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIFormat: llm.APIFormatChatCompletions, BaseURL: server.URL, APIKey: "test", Model: name, Timeout: time.Second}})
				}
				runtime := NewRuntime(BotConfig{ModelRequestTimeout: 60 * time.Millisecond}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: set}, nil, nil, nil)
				if registered {
					registry, _, err := llm.NewProviderRegistryFromProfiles(set)
					if err != nil {
						t.Fatal(err)
					}
					runtime.SetLLMProviderRegistry(registry)
				} else {
					runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) { return llm.NewClient(cfg) })
				}
				parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				text, err := runtime.runRawLLMProviderForGroup(parent, llm.GroupChat, func(provider LLMProvider) (string, error) {
					if streaming {
						provider = &streamingLLMProvider{provider: provider}
					}
					resp, err := provider.Generate(parent, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}}})
					if err != nil {
						return "", err
					}
					return resp.Text, nil
				})
				if err != nil || text != "ok" {
					t.Fatalf("text=%q err=%v", text, err)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, profile := range set.Profiles {
					if calls[profile.Config.Model] != 1 {
						t.Fatalf("calls=%v", calls)
					}
				}
				for _, health := range runtime.llmCooldowns.snapshot(set.Profiles) {
					if health.failures != 0 || health.remaining != 0 {
						t.Fatalf("unexpected health penalty=%+v", health)
					}
				}
			})
		}
	}
}

func TestStreamCancellationDoesNotWaitForChannelClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := accumulateChatEvents(ctx, make(chan llm.ChatEvent)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
