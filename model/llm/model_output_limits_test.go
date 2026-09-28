// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testModelsDevOutputLimits 是测试用的 models.dev 片段。测试不许真的去拉 models.dev：
// init 里就把目录换成这份本地数据，且标成刚取过，后台刷新不会触发。
var testModelsDevOutputLimits = map[string]map[string]ModelInfo{
	"anthropic": {
		"claude-opus-4-6":            {ID: "claude-opus-4-6", MaxOutputTokens: 128000},
		"claude-3-5-sonnet-20241022": {ID: "claude-3-5-sonnet-20241022", MaxOutputTokens: 8192},
	},
	"google": {
		"gemini-2.5-flash": {ID: "gemini-2.5-flash", MaxOutputTokens: 65536},
		"gemini-2.0-flash": {ID: "gemini-2.0-flash", MaxOutputTokens: 8192},
	},
	"deepseek": {
		"deepseek-chat": {ID: "deepseek-chat", MaxOutputTokens: 8192},
	},
}

func init() {
	outputLimitCatalog = &ModelsDevCatalog{providers: testModelsDevOutputLimits, fetchedAt: time.Now().Add(100 * 365 * 24 * time.Hour)}
}

func TestResolveMaxOutputTokensLikeOpencode(t *testing.T) {
	// antigravity 这类网关给每个模型报的占位值，不参与取值：只信 models.dev。
	catalog := []ModelInfo{{ID: "claude-opus-4-6", MaxOutputTokens: 8192}}
	deepseek := ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions, BaseURL: "https://api.deepseek.com/v1"}
	relay := ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions, BaseURL: "https://relay.example.com/v1"}
	for _, item := range []struct {
		name       string
		cfg        ProviderConfig
		model      string
		want       int64
		wantSource MaxOutputTokensSource
	}{
		{"用户填的值优先，可以超过封顶", ProviderConfig{Provider: ProviderAnthropic, MaxOutputTokens: 128000}, "claude-opus-4-6", 128000, MaxOutputTokensSourceUser},
		{"models.dev 的上限按 32000 封顶", ProviderConfig{Provider: ProviderAnthropic, Models: catalog}, "claude-opus-4-6", DefaultOutputTokenCeiling, MaxOutputTokensSourceModelsDev},
		{"models.dev 更小的上限照用", ProviderConfig{Provider: ProviderAnthropic}, "claude-3-5-sonnet-20241022", 8192, MaxOutputTokensSourceModelsDev},
		{"models.dev 查不到按 32000", ProviderConfig{Provider: ProviderAnthropic}, "relay-claude", DefaultOutputTokenCeiling, MaxOutputTokensSourceDefault},
		{"Gemini 按 google 查", ProviderConfig{Provider: ProviderGemini}, "gemini-2.5-flash", DefaultOutputTokenCeiling, MaxOutputTokensSourceModelsDev},
		{"网关加的档位后缀去掉再查", ProviderConfig{Provider: ProviderGemini}, "gemini-2.0-flash-low", 8192, MaxOutputTokensSourceModelsDev},
		{"带命名空间和档位后缀", ProviderConfig{Provider: ProviderGemini}, "models/gemini-2.0-flash-thinking", 8192, MaxOutputTokensSourceModelsDev},
		{"去掉后缀还查不到按 32000", ProviderConfig{Provider: ProviderGemini}, "gemini-9-flash-low", DefaultOutputTokenCeiling, MaxOutputTokensSourceDefault},
		{"Chat Completions 按地址认服务商", deepseek, "deepseek-chat", 8192, MaxOutputTokensSourceModelsDev},
		{"认不出服务商的中转按 32000", relay, "deepseek-chat", DefaultOutputTokenCeiling, MaxOutputTokensSourceDefault},
		{"Responses 不发", ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatResponses}, "gpt-5.5", 0, MaxOutputTokensSourceProvider},
		{"没传模型时看配置档的默认模型", ProviderConfig{Provider: ProviderGemini, Model: "gemini-2.0-flash"}, "", 8192, MaxOutputTokensSourceModelsDev},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, source := item.cfg.ResolveMaxOutputTokens(item.model)
			if got != item.want || source != item.wantSource {
				t.Fatalf("ResolveMaxOutputTokens = %d, %q; want %d, %q", got, source, item.want, item.wantSource)
			}
		})
	}
}

// 封顶值可以由 config.yaml 调整，0 恢复默认。
func TestOutputTokenCeilingConfigurable(t *testing.T) {
	t.Cleanup(func() { SetOutputTokenCeiling(0) })
	SetOutputTokenCeiling(64000)
	cfg := ProviderConfig{Provider: ProviderAnthropic}
	if got, _ := cfg.ResolveMaxOutputTokens("claude-opus-4-6"); got != 64000 {
		t.Fatalf("raised ceiling = %d, want 64000", got)
	}
	if got, _ := cfg.ResolveMaxOutputTokens("relay-claude"); got != 64000 {
		t.Fatalf("unknown model = %d, want the raised ceiling 64000", got)
	}
	SetOutputTokenCeiling(0)
	if got := OutputTokenCeiling(); got != DefaultOutputTokenCeiling {
		t.Fatalf("reset ceiling = %d, want %d", got, DefaultOutputTokenCeiling)
	}
}

// OutputLimit 在请求路径上只读缓存：缓存为空时不等网络，后台取回后才查得到。
func TestModelsDevOutputLimitRefreshesInBackground(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"anthropic":{"models":{"claude-x":{"limit":{"output":20000}}}}}`))
	}))
	defer server.Close()
	catalog := newModelsDevCatalog(server.Client(), server.URL)
	cfg := ProviderConfig{Provider: ProviderAnthropic}
	if _, ok := catalog.OutputLimit(cfg, "claude-x"); ok {
		t.Fatal("empty cache should miss instead of waiting for the network")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if limit, ok := catalog.OutputLimit(cfg, "claude-x"); ok {
			if limit != 20000 {
				t.Fatalf("limit = %d, want 20000", limit)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background refresh never filled the cache")
}

// 代发值只下发，不进预算；按上下文剩余空间收紧时，至少留出预算预留的那份。
func TestImplicitMaxOutputTokensClampsToContextRoom(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderAnthropic}
	big := GenerateRequest{Model: "claude-opus-4-6", MaxContextTokens: 128000, Messages: []Message{{Role: RoleUser, Content: strings.Repeat("a", 300000)}}}
	room := big.MaxContextTokens - estimateRequestInputTokens(big) - contextBudgetSafetyReserve
	if room >= DefaultOutputTokenCeiling || room <= DefaultMaxOutputTokens {
		t.Fatalf("fixture room = %d, want it between the reserve and the ceiling", room)
	}
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, big, true); got.MaxOutputTokens != room || !got.implicitMaxOutputTokens {
		t.Fatalf("clamped = %d (implicit %t), want room %d", got.MaxOutputTokens, got.implicitMaxOutputTokens, room)
	}
	full := GenerateRequest{Model: "claude-opus-4-6", MaxContextTokens: 128000, Messages: []Message{{Role: RoleUser, Content: strings.Repeat("a", 600000)}}}
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, full, true); got.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Fatalf("no room left = %d, want the reserved %d", got.MaxOutputTokens, DefaultMaxOutputTokens)
	}
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, big, false); got.MaxOutputTokens != DefaultOutputTokenCeiling {
		t.Fatalf("unclamped = %d, want %d", got.MaxOutputTokens, DefaultOutputTokenCeiling)
	}
	explicit := big
	explicit.MaxOutputTokens = 300
	if got := cfg.withImplicitMaxOutputTokens(ProviderAnthropic, explicit, true); got.MaxOutputTokens != 300 || got.implicitMaxOutputTokens {
		t.Fatalf("explicit = %d (implicit %t), want untouched 300", got.MaxOutputTokens, got.implicitMaxOutputTokens)
	}
}

// 上下文预算只为输出预留 DefaultMaxOutputTokens：代发 64K 的上限不能把 128K 的兜底
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
	if maxTokens <= 8192 || maxTokens > float64(DefaultOutputTokenCeiling) {
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

// 代发的默认值超出模型上限时，按报错里写的上限重发一次；用户填的值被拒照实报错。
func TestAnthropicImplicitMaxTokensRetriesWithReportedLimit(t *testing.T) {
	for _, item := range []struct {
		name      string
		userLimit int64
		wantSent  []float64
		wantErr   bool
	}{
		{name: "implicit", wantSent: []float64{32000, 20000}},
		{name: "user configured", userLimit: 100000, wantSent: []float64{100000}, wantErr: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			var sent []float64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				maxTokens, _ := body["max_tokens"].(float64)
				sent = append(sent, maxTokens)
				w.Header().Set("Content-Type", "application/json")
				if maxTokens > 20000 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: ` + strconv.FormatFloat(maxTokens, 'f', 0, 64) + ` > 20000, which is the maximum allowed number of output tokens for relay-claude"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"relay-claude","content":[{"type":"text","text":"好"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()
			client := newAnthropicClient(ProviderConfig{Provider: ProviderAnthropic, APIKey: "test", BaseURL: server.URL, Model: "relay-claude", MaxOutputTokens: item.userLimit, Timeout: time.Minute}, server.Client())
			_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
			if (err != nil) != item.wantErr || !slices.Equal(sent, item.wantSent) {
				t.Fatalf("err=%v sent=%v, want err=%t sent=%v", err, sent, item.wantErr, item.wantSent)
			}
		})
	}
}
