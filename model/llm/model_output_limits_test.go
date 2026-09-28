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

// testModelsDevOutputLimits 是测试用的 models.dev 片段：测试不依赖随版本打包的快照
// 内容，init 里就把目录换成这份固定数据。
var testModelsDevOutputLimits = map[string]map[string]ModelInfo{
	"anthropic": {
		"claude-opus-4-6":            {ID: "claude-opus-4-6", MaxOutputTokens: 128000},
		"claude-3-5-sonnet-20241022": {ID: "claude-3-5-sonnet-20241022", MaxOutputTokens: 8192},
	},
	"google": {
		"gemini-2.5-flash": {ID: "gemini-2.5-flash", MaxOutputTokens: 65536, ContextWindowTokens: 1048576},
		"gemini-2.0-flash": {ID: "gemini-2.0-flash", MaxOutputTokens: 8192},
		// 专门用来验证前缀匹配取最长 ID：relay-gemini-2.5-flash 也以 -2.5-flash 结尾。
		"2.5-flash": {ID: "2.5-flash", MaxOutputTokens: 1000, ContextWindowTokens: 4096},
	},
	"deepseek": {
		"deepseek-chat": {ID: "deepseek-chat", MaxOutputTokens: 8192, ContextWindowTokens: 131072},
	},
	// 智谱在 models.dev 里是同一主机的两家：普通接口和编程套餐，靠路径区分。
	"zhipuai": {
		"glm-4.5v": {ID: "glm-4.5v", MaxOutputTokens: 16384, ContextWindowTokens: 64000},
	},
	"zhipuai-coding-plan": {
		"glm-4.5v": {ID: "glm-4.5v", MaxOutputTokens: 8000, ContextWindowTokens: 32000},
	},
}

var testModelsDevAPIs = map[string]string{
	"zhipuai":             "https://open.bigmodel.cn/api/paas/v4",
	"zhipuai-coding-plan": "https://open.bigmodel.cn/api/coding/paas/v4",
}

func init() {
	catalog := &ModelsDevCatalog{providers: testModelsDevOutputLimits, apis: testModelsDevAPIs}
	catalog.once.Do(func() {})
	modelLimitCatalog = catalog
}

func TestResolveMaxOutputTokensOnlyWhenNeeded(t *testing.T) {
	for _, item := range []struct {
		name       string
		cfg        ProviderConfig
		want       int64
		wantSource MaxOutputTokensSource
	}{
		{"用户填的值照发", ProviderConfig{Provider: ProviderGemini, MaxOutputTokens: 4096}, 4096, MaxOutputTokensSourceUser},
		{"Anthropic 必填，按封顶发", ProviderConfig{Provider: ProviderAnthropic}, DefaultOutputTokenCeiling, MaxOutputTokensSourceDefault},
		{"Gemini 不发", ProviderConfig{Provider: ProviderGemini}, 0, MaxOutputTokensSourceProvider},
		{"Chat Completions 不发", ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions}, 0, MaxOutputTokensSourceProvider},
		{"Responses 不发", ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatResponses}, 0, MaxOutputTokensSourceProvider},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, source := item.cfg.ResolveMaxOutputTokens("gemini-2.5-flash")
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
	if got, _ := cfg.ResolveMaxOutputTokens("relay-claude"); got != 64000 {
		t.Fatalf("raised ceiling = %d, want 64000", got)
	}
	SetOutputTokenCeiling(0)
	if got := OutputTokenCeiling(); got != DefaultOutputTokenCeiling {
		t.Fatalf("reset ceiling = %d, want %d", got, DefaultOutputTokenCeiling)
	}
}

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

// Chat Completions 没填就不发 max_tokens，填了照发。
func TestChatCompletionsSendsMaxTokensOnlyWhenConfigured(t *testing.T) {
	for _, item := range []struct {
		name      string
		userLimit int64
		want      any
	}{
		{name: "unset", want: nil},
		{name: "user configured", userLimit: 4096, want: float64(4096)},
	} {
		t.Run(item.name, func(t *testing.T) {
			var sent any = "not called"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				sent = body["max_tokens"]
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chat_1","model":"glm-4v-flash","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions, Model: "glm-4v-flash", MaxOutputTokens: item.userLimit}
			if _, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
				t.Fatal(err)
			}
			if sent != item.want {
				t.Fatalf("max_tokens sent = %v, want %v", sent, item.want)
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

// 没填窗口时按 models.dev 的 limit.context，按配置的 API 地址认服务商。
func TestContextWindowFromModelsDev(t *testing.T) {
	zhipu := ProviderConfig{Provider: ProviderOpenAICompatible, APIFormat: APIFormatChatCompletions, BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.5v"}
	coding := zhipu
	coding.BaseURL = "https://open.bigmodel.cn/api/coding/paas/v4/"
	for _, item := range []struct {
		name       string
		cfg        ProviderConfig
		want       int64
		wantSource ContextWindowSource
	}{
		{"手填优先", ProviderConfig{Provider: ProviderGemini, Model: "gemini-2.5-flash", ContextWindowTokens: 200000}, 200000, ContextWindowSourceUser},
		{"Gemini 按 google 查", ProviderConfig{Provider: ProviderGemini, Model: "gemini-2.5-flash-low"}, 1048576, ContextWindowSourceModelsDev},
		{"按地址认出智谱", zhipu, 64000, ContextWindowSourceModelsDev},
		{"同主机按路径认出编程套餐", coding, 32000, ContextWindowSourceModelsDev},
		{"认不出的中转在所有服务商里找", ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: "https://relay.example.com/v1", Model: "deepseek-chat"}, 131072, ContextWindowSourceModelsDev},
		{"查不到按兜底", ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4v-flash"}, DefaultContextWindowTokens, ContextWindowSourceFallback},
		{"命名空间和档位后缀", ProviderConfig{Provider: ProviderGemini, Model: "models/gemini-2.5-flash-thinking"}, 1048576, ContextWindowSourceModelsDev},
		{"网关在前面加的标记", ProviderConfig{Provider: ProviderGemini, Model: "antigravity-gemini-2.5-flash"}, 1048576, ContextWindowSourceModelsDev},
		{"前缀取最长的 ID，不被短 ID 误配", ProviderConfig{Provider: ProviderGemini, Model: "relay-gemini-2.5-flash"}, 1048576, ContextWindowSourceModelsDev},
		{"没有分隔符的长 ID 不算，退到短 ID", ProviderConfig{Provider: ProviderGemini, Model: "xgemini-2.5-flash"}, 4096, ContextWindowSourceModelsDev},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, source := item.cfg.ResolveContextWindowTokens()
			if got != item.want || source != item.wantSource {
				t.Fatalf("ResolveContextWindowTokens = %d, %q; want %d, %q", got, source, item.want, item.wantSource)
			}
		})
	}
	// 同一套配置里换个模型，预算按这次实际用的模型算。
	cfg := ProviderConfig{Provider: ProviderGemini, Model: "house-gemini"}
	req := applyContextBudget(GenerateRequest{Model: "gemini-2.5-flash", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, cfg)
	if req.MaxContextTokens != 1048576 {
		t.Fatalf("budget for the requested model = %d, want 1048576", req.MaxContextTokens)
	}
	// 手填的值恰好等于目录值也要保留，不能在落库时被当成派生值清掉。
	kept := ProviderConfig{Provider: ProviderGemini, Model: "gemini-2.5-flash", ContextWindowTokens: 1048576}.WithoutRedundantContextLimits()
	if kept.ContextWindowTokens != 1048576 {
		t.Fatalf("user window stripped: %d", kept.ContextWindowTokens)
	}
}
