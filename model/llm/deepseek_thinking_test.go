// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// DeepSeek 不认 reasoning_effort=none，关思考要发 thinking.type=disabled；
// 其他档位折算成 low/high/max 并显式开启思考。其他端点保持原样。
func TestChatCompletionsDeepSeekThinkingToggle(t *testing.T) {
	cases := []struct {
		name         string
		model        string
		effort       string
		wantEffort   any
		wantThinking any
	}{
		{name: "deepseek none disables thinking", model: "deepseek-v4-flash", effort: "none", wantEffort: nil, wantThinking: map[string]any{"type": "disabled"}},
		{name: "deepseek low enables thinking", model: "deepseek-v4-flash", effort: "low", wantEffort: "low", wantThinking: map[string]any{"type": "enabled"}},
		{name: "deepseek minimal folds to low", model: "deepseek-v4-flash", effort: "minimal", wantEffort: "low", wantThinking: map[string]any{"type": "enabled"}},
		{name: "deepseek medium folds to high", model: "deepseek-v4-flash", effort: "medium", wantEffort: "high", wantThinking: map[string]any{"type": "enabled"}},
		{name: "deepseek xhigh folds to high", model: "deepseek-v4-flash", effort: "xhigh", wantEffort: "high", wantThinking: map[string]any{"type": "enabled"}},
		{name: "deepseek ultra folds to max", model: "deepseek-v4-flash", effort: "ultra", wantEffort: "max", wantThinking: map[string]any{"type": "enabled"}},
		{name: "deepseek-chat max turns thinking on", model: "deepseek-chat", effort: "max", wantEffort: "max", wantThinking: map[string]any{"type": "enabled"}},
		{name: "other endpoint keeps medium", model: "gpt-5.1", effort: "medium", wantEffort: "medium", wantThinking: nil},
		{name: "deepseek default sends nothing", model: "deepseek-v4-flash", effort: "", wantEffort: nil, wantThinking: nil},
		{name: "other endpoint keeps none", model: "gpt-5.1", effort: "none", wantEffort: "none", wantThinking: nil},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				var body map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewDecoder(r.Body).Decode(&body)
					if stream {
						writeChatEvents(w, `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
				}))
				defer server.Close()
				client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", BaseURL: server.URL, Model: tc.model, APIFormat: APIFormatChatCompletions, ReasoningEffort: tc.effort}, server.Client())
				req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
				if stream {
					events, err := client.Stream(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					for e := range events {
						if e.Type == ChatEventError {
							t.Fatal(e.Error)
						}
					}
				} else if _, err := client.Generate(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				if got := body["reasoning_effort"]; got != tc.wantEffort {
					t.Fatalf("stream=%v reasoning_effort = %#v, want %#v", stream, got, tc.wantEffort)
				}
				gotThinking, _ := json.Marshal(body["thinking"])
				wantThinking, _ := json.Marshal(tc.wantThinking)
				if string(gotThinking) != string(wantThinking) {
					t.Fatalf("stream=%v thinking = %s, want %s", stream, gotThinking, wantThinking)
				}
			})
		}
	}
}
