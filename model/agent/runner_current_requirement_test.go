// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type currentRequirementContinuationClient struct {
	adapter  llm.LLMClient
	requests []llm.GenerateRequest
}

func (c *currentRequirementContinuationClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	req.Messages = append([]llm.Message(nil), req.Messages...)
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{Text: `{"action":"tool","tool":"lookup","input":{}}`}, nil
	}
	return c.adapter.Generate(ctx, req)
}

// Exercise real JSON tool observations and both planning/finalization requests.
// The original task stays pinned as current input; when it cannot fit even
// alone, the adapter shortens it rather than failing the turn (#518).
func TestRunnerKeepsOriginalCurrentRequirementAcrossContinuationBudget(t *testing.T) {
	for _, phase := range []string{"planning", "finalization"} {
		for _, tooSmall := range []bool{false, true} {
			name := phase + "/fits"
			if tooSmall {
				name = phase + "/shortened"
			}
			t.Run(name, func(t *testing.T) {
				original := "CURRENT_HEAD " + strings.Repeat("original requirement ", 1000) + " CRITICAL_MIDDLE_REQUIREMENT " + strings.Repeat("keep requirement ", 1000) + " CURRENT_TAIL"
				history := strings.Repeat("old optional history ", 20000)
				var httpCalls atomic.Int32
				bodies := make(chan string, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					httpCalls.Add(1)
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Error(err)
					}
					bodies <- string(body)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "continuation", "choices": []any{map[string]any{
						"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"action":"final","content":"done"}`},
					}}})
				}))
				defer server.Close()
				window := int64(32768)
				if tooSmall {
					window = 2048
				}
				adapter, err := llm.NewClient(llm.ProviderConfig{
					Provider: llm.ProviderOpenAICompatible, APIFormat: llm.APIFormatChatCompletions,
					APIKey: "test-key", Model: "test-model", BaseURL: server.URL,
					ContextWindowTokens: window, MaxContextTokens: window, MaxOutputTokens: 128,
				}, llm.WithHTTPClient(server.Client()))
				if err != nil {
					t.Fatal(err)
				}
				client := &currentRequirementContinuationClient{adapter: adapter}
				tool := &countingTool{name: "lookup"}
				steps := 2
				if phase == "finalization" {
					steps = 1
				}
				runner, err := NewRunner(client, Config{MaxSteps: steps}, NewToolRegistry(tool))
				if err != nil {
					t.Fatal(err)
				}
				messages := []llm.Message{
					{Role: llm.RoleUser, Content: history, Priority: llm.MessagePriorityHistory},
					{Role: llm.RoleAssistant, Content: "earlier reply", Priority: llm.MessagePriorityHistory},
					{Role: llm.RoleUser, Content: original},
				}
				response, err := runner.Run(t.Context(), Request{Messages: messages})
				if messages[2].Priority != llm.MessagePriorityDefault || messages[0].Priority != llm.MessagePriorityHistory {
					t.Fatal("runner mutated the caller's priorities")
				}
				if len(client.requests) != 2 || tool.calls != 1 {
					t.Fatalf("requests=%d tool_calls=%d", len(client.requests), tool.calls)
				}
				for _, request := range client.requests {
					originalFound := false
					for _, message := range request.Messages {
						if message.Content == original {
							originalFound = message.Priority == llm.MessagePriorityCurrent
						}
						if message.Content == history && message.Priority != llm.MessagePriorityHistory {
							t.Fatal("old history was promoted to current")
						}
					}
					if !originalFound {
						t.Fatal("continuation lost the original current requirement")
					}
				}
				if tooSmall {
					if err != nil || response == nil || response.Text != "done" || httpCalls.Load() != 1 {
						t.Fatalf("oversized current input should be shortened and sent: response=%+v err=%v http_calls=%d", response, err, httpCalls.Load())
					}
					body := <-bodies
					if !strings.Contains(body, "CURRENT_HEAD") || strings.Contains(body, "old optional history") {
						t.Fatal("shortened payload lost the current task head or kept optional history")
					}
					return
				}
				if err != nil || response == nil || response.Text != "done" {
					t.Fatalf("response=%+v err=%v", response, err)
				}
				if httpCalls.Load() != 1 {
					t.Fatalf("http_calls=%d", httpCalls.Load())
				}
				body := <-bodies
				if !strings.Contains(body, original) || strings.Contains(body, history) {
					t.Fatal("provider payload changed the current task or retained oversized optional history")
				}
			})
		}
	}
}

func TestRunnerRespectsExplicitCurrentRequirementBoundary(t *testing.T) {
	client := &scriptedClient{responses: []string{`{"action":"final","content":"done"}`}}
	runner, err := NewRunner(client, Config{MaxSteps: 1}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "explicit current", Priority: llm.MessagePriorityCurrent},
		{Role: llm.RoleUser, Content: "caller-owned supporting context", Priority: llm.MessagePrioritySystem},
	}
	if _, err := runner.Run(t.Context(), Request{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	for _, message := range client.requests[0].Messages {
		if message.Content == messages[1].Content && message.Priority != llm.MessagePrioritySystem {
			t.Fatal("runner replaced the caller's explicit current boundary")
		}
	}
}
