// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuiltinMaxOutputTokensMatchesFamilies(t *testing.T) {
	for _, item := range []struct {
		model string
		want  int64
		ok    bool
	}{
		// 网关给的档位和日期后缀落在家族前缀后面。
		{"gemini-3.8-flash-low", 65536, true},
		{"models/gemini-3.8-flash-low", 65536, true},
		{"gemini-3-pro-image-4k", 32768, true},
		{"claude-opus-4.6-thinking", 128000, true},
		{"claude-opus-4-5-20251101", 64000, true},
		{"claude-opus-4-1", 32000, true},
		{"anthropic/claude-sonnet-4-5", 64000, true},
		{"gpt-5.5", 128000, true},
		{"gpt-5-pro", 272000, true},
		{"gpt-5.2-pro", 128000, true},
		{"gpt-5.2-chat-latest", 16384, true},
		{"openai/gpt-4o-mini", 16384, true},
		{"o3-pro", 100000, true},
		{"glm-4.5v", 16384, true},
		{"glm-4.5-air", 98304, true},
		{"deepseek-flash", 384000, true},
		{"MiniMax-M2.7", 131072, true},
		// 前缀后面必须是分隔符，也不认没有版本的裸家族名。
		{"gpt-50", 0, false},
		{"o3x", 0, false},
		{"claude", 0, false},
		{"jev-latest", 0, false},
		{"", 0, false},
	} {
		got, ok := BuiltinMaxOutputTokens(item.model)
		if got != item.want || ok != item.ok {
			t.Errorf("BuiltinMaxOutputTokens(%q) = %d, %t; want %d, %t", item.model, got, ok, item.want, item.ok)
		}
	}
}

func TestResolveMaxOutputTokensByProtocol(t *testing.T) {
	catalog := []ModelInfo{
		// antigravity 这类网关给每个模型报的占位值，不能压过内置表。
		{ID: "claude-sonnet-4-6", MaxOutputTokens: 8192},
		{ID: "relay-claude", MaxOutputTokens: 4096},
		{ID: "relay-empty"},
	}
	for _, item := range []struct {
		name       string
		cfg        ProviderConfig
		model      string
		want       int64
		wantSource MaxOutputTokensSource
	}{
		{"用户填的值优先", ProviderConfig{Provider: ProviderAnthropic, MaxOutputTokens: 2048}, "claude-sonnet-4-6", 2048, MaxOutputTokensSourceUser},
		{"Anthropic 内置表压过清单占位值", ProviderConfig{Provider: ProviderAnthropic, Models: catalog}, "claude-sonnet-4-6", 128000, MaxOutputTokensSourceBuiltin},
		{"Anthropic 表里没有才用清单", ProviderConfig{Provider: ProviderAnthropic, Models: catalog}, "relay-claude", 4096, MaxOutputTokensSourceCatalog},
		{"Anthropic 清单没写上限不能发 0", ProviderConfig{Provider: ProviderAnthropic, Models: catalog}, "relay-empty", defaultAnthropicMaxTokens, MaxOutputTokensSourceFallback},
		{"Gemini 按表", ProviderConfig{Provider: ProviderGemini}, "gemini-3.8-flash-low", 65536, MaxOutputTokensSourceBuiltin},
		{"Gemini 不认识的名字按现行上限", ProviderConfig{Provider: ProviderGemini}, "claude", 65536, MaxOutputTokensSourceFallback},
		{"Chat Completions 按表", ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions}, "deepseek-flash", 384000, MaxOutputTokensSourceBuiltin},
		{"Chat Completions 不认识的不发", ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions}, "jev-latest", 0, MaxOutputTokensSourceProvider},
		{"Responses 不发", ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatResponses}, "gpt-5.5", 0, MaxOutputTokensSourceProvider},
		{"没传模型时看配置档的默认模型", ProviderConfig{Provider: ProviderGemini, Model: "gemini-2.0-flash"}, "", 8192, MaxOutputTokensSourceBuiltin},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, source := item.cfg.ResolveMaxOutputTokens(item.model)
			if got != item.want || source != item.wantSource {
				t.Fatalf("ResolveMaxOutputTokens = %d, %q; want %d, %q", got, source, item.want, item.wantSource)
			}
		})
	}
}

// 代发值只下发，不进预算；按上下文剩余空间收紧时，至少留出预算预留的那份。
func TestImplicitMaxOutputTokensClampsToContextRoom(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderAnthropic}
	big := GenerateRequest{Model: "claude-opus-4-6", MaxContextTokens: 128000, Messages: []Message{{Role: RoleUser, Content: strings.Repeat("a", 200000)}}}
	room := big.MaxContextTokens - estimateRequestInputTokens(big) - contextBudgetSafetyReserve
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, big, true); got.MaxOutputTokens != room || !got.implicitMaxOutputTokens {
		t.Fatalf("clamped = %d (implicit %t), want room %d", got.MaxOutputTokens, got.implicitMaxOutputTokens, room)
	}
	full := GenerateRequest{Model: "claude-opus-4-6", MaxContextTokens: 128000, Messages: []Message{{Role: RoleUser, Content: strings.Repeat("a", 600000)}}}
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, full, true); got.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("no room left = %d, want the reserved %d", got.MaxOutputTokens, DefaultMaxOutputTokens)
	}
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, big, false); got.MaxOutputTokens != 128000 {
		t.Fatalf("unclamped = %d, want 128000", got.MaxOutputTokens)
	}
	explicit := big
	explicit.MaxOutputTokens = 300
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, explicit, true); got.MaxOutputTokens != 300 || got.implicitMaxOutputTokens {
		t.Fatalf("explicit = %d (implicit %t), want untouched 300", got.MaxOutputTokens, got.implicitMaxOutputTokens)
	}
}

// 上下文预算只为输出预留 DefaultMaxOutputTokens：代发 128K 的上限不能把 128K 的兜底
// 窗口里的历史挤掉。
func TestAnthropicSendsModelMaxWithoutShrinkingHistory(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"好"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	client := newAnthropicClient(ProviderConfig{Provider: ProviderAnthropic, APIKey: "test", BaseURL: server.URL, Model: "claude-opus-4-6", Models: []ModelInfo{{ID: "claude-opus-4-6", MaxOutputTokens: 8192}}}, server.Client())
	history := make([]Message, 0, 40)
	for i := 0; i < 40; i++ {
		history = append(history, Message{Role: RoleUser, Content: strings.Repeat("历史", 1000)})
	}
	if _, err := client.Generate(context.Background(), GenerateRequest{Messages: history}); err != nil {
		t.Fatal(err)
	}
	maxTokens, _ := body["max_tokens"].(float64)
	if maxTokens <= 8192 || maxTokens > 128000 {
		t.Fatalf("max_tokens = %v, want the model max clamped to the context room", body["max_tokens"])
	}
	if messages, _ := body["messages"].([]any); len(messages) != len(history) {
		t.Fatalf("messages sent = %d, want all %d kept", len(messages), len(history))
	}
}

// Chat Completions 按表代发 max_tokens；上游不认时摘掉重发并记住，用户自己填的值
// 被拒则照实报错。
func TestChatCompletionsImplicitMaxTokensDowngrade(t *testing.T) {
	for _, item := range []struct {
		name      string
		userLimit int64
		wantErr   bool
		wantCalls int
	}{
		{name: "implicit", wantCalls: 2},
		{name: "user configured", userLimit: 50000, wantErr: true, wantCalls: 1},
	} {
		t.Run(item.name, func(t *testing.T) {
			var sent []any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				sent = append(sent, body["max_tokens"])
				w.Header().Set("Content-Type", "application/json")
				if body["max_tokens"] != nil {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 8192]","type":"invalid_request_error"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"chat_1","model":"deepseek-flash","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions, Model: "deepseek-flash", MaxOutputTokens: item.userLimit}
			req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
			_, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), req)
			if (err != nil) != item.wantErr || len(sent) != item.wantCalls {
				t.Fatalf("err=%v sent=%v, want err=%t calls=%d", err, sent, item.wantErr, item.wantCalls)
			}
			if item.wantErr {
				return
			}
			if first, _ := sent[0].(float64); first <= 0 {
				t.Fatalf("first attempt max_tokens = %v, want the builtin limit", sent[0])
			}
			// 新 client 沿用结论，一次就过。
			if _, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), req); err != nil || len(sent) != 3 || sent[2] != nil {
				t.Fatalf("remembered downgrade not applied: err=%v sent=%v", err, sent)
			}
		})
	}
}
