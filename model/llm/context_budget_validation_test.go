// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type budgetProtocolClient interface {
	LLMClient
	Stream(context.Context, GenerateRequest) (<-chan ChatEvent, error)
}

func budgetAdapterClient(t *testing.T, protocol string, cfg ProviderConfig, httpClient *http.Client) budgetProtocolClient {
	t.Helper()
	switch protocol {
	case "responses":
		cfg.Provider, cfg.APIFormat = ProviderOpenAICompatible, APIFormatResponses
		return newOpenAICompatibleClient(cfg, httpClient)
	case "chat":
		cfg.Provider, cfg.APIFormat = ProviderOpenAICompatible, APIFormatChatCompletions
		return newOpenAICompatibleClient(cfg, httpClient)
	case "anthropic":
		cfg.Provider = ProviderAnthropic
		return newAnthropicClient(cfg, httpClient)
	case "gemini":
		cfg.Provider = ProviderGemini
		client, err := newGeminiClient(cfg, httpClient)
		if err != nil {
			t.Fatal(err)
		}
		return client
	default:
		t.Fatalf("unknown protocol %s", protocol)
		return nil
	}
}

func runBudgetAdapter(t *testing.T, client budgetProtocolClient, req GenerateRequest, stream bool) error {
	t.Helper()
	if !stream {
		_, err := client.Generate(context.Background(), req)
		return err
	}
	events, err := client.Stream(context.Background(), req)
	if err != nil {
		return err
	}
	for event := range events {
		if event.Type == ChatEventError {
			return fmt.Errorf("stream failed: %s", event.Error)
		}
	}
	return nil
}

func TestBudgetAdaptersRejectImpossibleCurrentInputBeforeHTTP(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "anthropic", "gemini"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%v", protocol, stream), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusTeapot)
				}))
				defer server.Close()
				client := budgetAdapterClient(t, protocol, ProviderConfig{Model: "test-model", APIKey: "test-key", BaseURL: server.URL, ContextWindowTokens: 2048, MaxContextTokens: 2048, MaxOutputTokens: 128}, server.Client())
				for _, atomicText := range []bool{false, true} {
					current := "head " + strings.Repeat("A", 12000) + " critical middle requirement " + strings.Repeat("B", 12000) + " tail"
					req := GenerateRequest{Messages: []Message{
						{Role: RoleSystem, Content: "system"},
						{Role: RoleUser, Content: "history", Priority: MessagePriorityHistory},
						{Role: RoleUser, Content: current, Priority: MessagePriorityCurrent, AtomicText: atomicText},
					}}
					err := runBudgetAdapter(t, client, req, stream)
					if err == nil || !strings.Contains(err.Error(), "input budget") {
						t.Fatalf("want local capacity error (atomic=%v), got %v", atomicText, err)
					}
					if strings.Contains(err.Error(), "critical middle") {
						t.Fatal("capacity error leaked input content")
					}
				}
				if calls.Load() != 0 {
					t.Fatalf("impossible original input reached HTTP %d times", calls.Load())
				}
			})
		}
	}
}

func TestBudgetAdaptersSendIntactCurrentAfterOptionalContextIsDropped(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "anthropic", "gemini"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%v", protocol, stream), func(t *testing.T) {
				bodies := make(chan string, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					bodies <- string(body)
					// No provider response simulation is needed: the request itself is
					// the assertion target, and 418 prevents transparent retries.
					w.WriteHeader(http.StatusTeapot)
				}))
				defer server.Close()
				client := budgetAdapterClient(t, protocol, ProviderConfig{Model: "test-model", APIKey: "test-key", BaseURL: server.URL, ContextWindowTokens: 2048, MaxContextTokens: 2048, MaxOutputTokens: 128}, server.Client())
				current := "CURRENT_HEAD " + strings.Repeat("A", 1200) + " CURRENT_MIDDLE_REQUIREMENT " + strings.Repeat("B", 1200) + " CURRENT_TAIL"
				_ = runBudgetAdapter(t, client, GenerateRequest{Messages: []Message{
					{Role: RoleSystem, Content: "system"},
					{Role: RoleUser, Content: strings.Repeat("optional_history ", 5000), Priority: MessagePriorityHistory},
					{Role: RoleUser, Content: current, Priority: MessagePriorityCurrent},
				}}, stream)
				var captured string
				select {
				case captured = <-bodies:
				default:
					t.Fatal("fitting request did not reach the provider")
				}
				if !strings.Contains(captured, current) {
					t.Fatal("provider wire lost current input or its middle requirement")
				}
				if strings.Contains(captured, strings.Repeat("optional_history ", 5000)) {
					t.Fatal("optional history was not reduced to fit")
				}
			})
		}
	}
}

func TestBudgetAdaptersKeepParallelToolBatchWhole(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "anthropic", "gemini"} {
		for _, stream := range []bool{false, true} {
			for _, payloadSize := range []int{8000, 80000} {
				t.Run(fmt.Sprintf("%s/stream_%v/payload_%d", protocol, stream, payloadSize), func(t *testing.T) {
					bodies := make(chan string, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						bodies <- string(body)
						w.WriteHeader(http.StatusTeapot)
					}))
					defer server.Close()
					client := budgetAdapterClient(t, protocol, ProviderConfig{Model: "test-model", APIKey: "test-key", BaseURL: server.URL, ContextWindowTokens: 10000, MaxContextTokens: 10000, MaxOutputTokens: 128}, server.Client())
					req := GenerateRequest{
						Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
						Messages: []Message{
							{Role: RoleSystem, Content: strings.Repeat("S", 25000)},
							{Role: RoleUser, Content: "old question", Priority: MessagePriorityHistory},
							{Role: RoleAssistant, Priority: MessagePriorityHistory, ContextGroup: "batch", ToolCalls: []ToolCall{
								{ID: "budget-call-a", Name: "lookup", Arguments: map[string]any{"payload": strings.Repeat("P", payloadSize)}},
								{ID: "budget-call-b", Name: "lookup", Arguments: map[string]any{"query": "short"}},
							}},
							{Role: RoleTool, Priority: MessagePriorityHistory, ToolCallID: "budget-call-a", ToolName: "lookup", Content: "first result", ContextGroup: "batch"},
							{Role: RoleTool, Priority: MessagePriorityHistory, ToolCallID: "budget-call-b", ToolName: "lookup", Content: "second result"},
							{Role: RoleUser, Content: "current", Priority: MessagePriorityCurrent},
						},
					}
					_ = runBudgetAdapter(t, client, req, stream)
					var body string
					select {
					case body = <-bodies:
					default:
						t.Fatal("fitting request did not reach the provider")
					}
					want := 2 // Each call ID occurs once in its call and once in its result.
					if payloadSize == 80000 {
						want = 0 // The oversized optional batch must be dropped whole.
					}
					for _, id := range []string{"budget-call-a", "budget-call-b"} {
						if got := strings.Count(body, id); got != want {
							t.Fatalf("wire has partial batch for %s: count=%d want=%d", id, got, want)
						}
					}
				})
			}
		}
	}
}
