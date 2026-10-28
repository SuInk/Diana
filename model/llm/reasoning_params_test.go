// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

func TestApplyAnthropicReasoning(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		effort     string
		maxTokens  int64
		forcedTool bool
		wantThink  string // "", adaptive, disabled, enabled:<budget>
		wantEffort anthropic.OutputConfigEffort
		keepTemp   bool
	}{
		{name: "unset sends nothing", model: "claude-opus-4-8", effort: "", maxTokens: 16000, keepTemp: true},
		{name: "current medium", model: "claude-opus-4-8", effort: "medium", maxTokens: 16000, wantThink: "adaptive", wantEffort: "medium", keepTemp: true},
		{name: "current none disables", model: "claude-sonnet-5", effort: "none", maxTokens: 16000, wantThink: "disabled", keepTemp: true},
		{name: "current forced tool keeps effort only", model: "claude-opus-5", effort: "high", maxTokens: 16000, forcedTool: true, wantEffort: "high", keepTemp: true},
		{name: "4.6 has no xhigh", model: "claude-opus-4-6", effort: "xhigh", maxTokens: 16000, wantThink: "adaptive", wantEffort: "high", keepTemp: true},
		{name: "minimal and ultra fold", model: "us.anthropic.claude-opus-4-7", effort: "ultra", maxTokens: 16000, wantThink: "adaptive", wantEffort: "max", keepTemp: true},
		{name: "always thinking none goes low", model: "claude-fable-5-1", effort: "none", maxTokens: 16000, wantEffort: "low", keepTemp: true},
		{name: "always thinking max", model: "claude-opus-5-5", effort: "max", maxTokens: 16000, wantEffort: "max", keepTemp: true},
		{name: "legacy budget", model: "claude-haiku-4-5", effort: "high", maxTokens: 64000, wantThink: "enabled:16384"},
		{name: "legacy budget capped at half", model: "claude-sonnet-4-5", effort: "max", maxTokens: 8000, wantThink: "enabled:4000"},
		{name: "legacy no room for thinking", model: "claude-haiku-4-5", effort: "low", maxTokens: 1500, keepTemp: true},
		{name: "legacy none disables", model: "claude-3-7-sonnet-latest", effort: "none", maxTokens: 8000, wantThink: "disabled", keepTemp: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := anthropic.MessageNewParams{Model: anthropic.Model(tc.model), MaxTokens: tc.maxTokens, Temperature: param.NewOpt(0.3)}
			if tc.forcedTool {
				params.ToolChoice = anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: "lookup"}}
			}
			applyAnthropicReasoning(&params, GenerateRequest{ReasoningEffort: tc.effort})
			got := ""
			switch {
			case params.Thinking.OfAdaptive != nil:
				got = "adaptive"
			case params.Thinking.OfDisabled != nil:
				got = "disabled"
			case params.Thinking.OfEnabled != nil:
				got = fmt.Sprintf("enabled:%d", params.Thinking.OfEnabled.BudgetTokens)
			}
			if got != tc.wantThink {
				t.Fatalf("thinking = %q, want %q", got, tc.wantThink)
			}
			if params.OutputConfig.Effort != tc.wantEffort {
				t.Fatalf("effort = %q, want %q", params.OutputConfig.Effort, tc.wantEffort)
			}
			if params.Temperature.Valid() != tc.keepTemp {
				t.Fatalf("temperature kept = %v, want %v", params.Temperature.Valid(), tc.keepTemp)
			}
		})
	}
}

func TestGeminiThinkingConfig(t *testing.T) {
	budget := func(v int32) string { return fmt.Sprintf("budget:%d", v) }
	cases := []struct {
		model, effort, want string
	}{
		{"gemini-3-flash-preview", "", ""},
		{"gemini-3-flash-preview", "none", "level:MINIMAL"},
		{"gemini-3-flash-preview", "medium", "level:MEDIUM"},
		{"gemini-3-flash-preview", "max", "level:HIGH"},
		{"gemini-3-pro-preview", "none", "level:LOW"},
		{"gemini-3-pro-preview", "medium", "level:HIGH"},
		{"gemini-2.5-flash", "none", budget(0)},
		{"gemini-2.5-pro", "none", budget(128)},
		{"gemini-2.5-flash", "high", budget(16384)},
		{"gemini-2.0-flash", "high", ""},
		{"gemma-3-27b-it", "high", ""},
	}
	for _, tc := range cases {
		cfg := geminiThinkingConfig(tc.model, tc.effort)
		got := ""
		switch {
		case cfg == nil:
		case cfg.ThinkingBudget != nil:
			got = budget(*cfg.ThinkingBudget)
		default:
			got = "level:" + string(cfg.ThinkingLevel)
		}
		if got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
}

// 退避：上游 400 拒了思考参数就摘掉重发，重发成功后记住，下一次直接不发。
func TestAnthropicReasoningBackoff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				bodies = append(bodies, body)
				if _, ok := body["thinking"]; ok {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking: adaptive thinking is not supported on this model"}}`)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range []string{
						`{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
						`{"type":"content_block_stop","index":0}`,
						`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
						`{"type":"message_stop"}`,
					} {
						var e map[string]any
						_ = json.Unmarshal([]byte(event), &e)
						fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], event)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer server.Close()
			client := newAnthropicClient(ProviderConfig{Provider: ProviderAnthropic, APIKey: "key", BaseURL: server.URL, Model: "claude-opus-4-8", ReasoningEffort: "low", MaxOutputTokens: 4096}, server.Client())
			run := func() {
				req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
				if !stream {
					if _, err := client.Generate(context.Background(), req); err != nil {
						t.Fatal(err)
					}
					return
				}
				events, err := client.Stream(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				for e := range events {
					if e.Type == ChatEventError {
						t.Fatal(e.Error)
					}
				}
			}
			run()
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want rejected + retry", len(bodies))
			}
			if _, ok := bodies[1]["output_config"]; ok {
				t.Fatalf("retry still carries effort: %#v", bodies[1])
			}
			run()
			if len(bodies) != 3 {
				t.Fatalf("remembered downgrade should skip the rejected attempt, requests = %d", len(bodies))
			}
		})
	}
}

// 历史里的工具调用缺思考块被拒：这一轮摘掉思考重发，但不记住，下一轮照发。
func TestAnthropicThinkingHistoryRejectionNotRemembered(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["thinking"]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0.type: Expected `+"`thinking`"+` or `+"`redacted_thinking`"+`, but found `+"`tool_use`"+`. When thinking is enabled, a final assistant message must start with a thinking block."}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	client := newAnthropicClient(ProviderConfig{Provider: ProviderAnthropic, APIKey: "key", BaseURL: server.URL, Model: "claude-opus-4-8", ReasoningEffort: "high", MaxOutputTokens: 4096}, server.Client())
	for range 2 {
		if _, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 4 {
		t.Fatalf("requests = %d, want each round to try thinking first", len(bodies))
	}
}

func TestGeminiReasoningBackoff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				raw, _ := json.Marshal(body)
				bodies = append(bodies, string(raw))
				if strings.Contains(string(raw), "thinkingConfig") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"code":400,"message":"Thinking level is not supported for this model.","status":"INVALID_ARGUMENT"}}`)
					return
				}
				payload := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: "+payload+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, payload)
			}))
			defer server.Close()
			client, err := newGeminiClient(ProviderConfig{Provider: ProviderGemini, APIKey: "key", BaseURL: server.URL, Model: "gemini-3-flash-preview", ReasoningEffort: "none"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			run := func() {
				req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
				if !stream {
					if _, err := client.Generate(context.Background(), req); err != nil {
						t.Fatal(err)
					}
					return
				}
				events, err := client.Stream(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				for e := range events {
					if e.Type == ChatEventError {
						t.Fatal(e.Error)
					}
				}
			}
			run()
			if len(bodies) != 2 || strings.Contains(bodies[1], "thinkingConfig") {
				t.Fatalf("bodies = %v", bodies)
			}
			run()
			if len(bodies) != 3 {
				t.Fatalf("remembered downgrade should skip the rejected attempt, requests = %d", len(bodies))
			}
		})
	}
}

func TestChatCompletionsReasoningBackoff(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["reasoning_effort"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", BaseURL: server.URL, Model: "gpt-4o", APIFormat: APIFormatChatCompletions, ReasoningEffort: "none"}, server.Client())
	for range 2 {
		if _, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want rejected + retry + remembered", len(bodies))
	}
}
