// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"errors"
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

// 超出窗口的请求不裁剪、不发出，本地报 ErrContextOverflow，错误里不带正文。
func TestBudgetAdaptersRejectOversizedRequestWithoutSending(t *testing.T) {
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
				current := "head " + strings.Repeat("A", 12000) + " critical middle requirement " + strings.Repeat("B", 12000) + " tail"
				err := runBudgetAdapter(t, client, GenerateRequest{Messages: []Message{
					{Role: RoleSystem, Content: "system"},
					{Role: RoleUser, Content: "history", Priority: MessagePriorityHistory},
					{Role: RoleUser, Content: current, Priority: MessagePriorityCurrent},
				}}, stream)
				if !errors.Is(err, ErrContextOverflow) {
					t.Fatalf("want ErrContextOverflow, got %v", err)
				}
				if strings.Contains(err.Error(), "critical middle") {
					t.Fatal("overflow error leaked input content")
				}
				if calls.Load() != 0 {
					t.Fatalf("oversized request reached HTTP %d times", calls.Load())
				}
			})
		}
	}
}

// 装得下的请求逐字发出：历史、工具批次都不动。
func TestBudgetAdaptersSendFittingRequestVerbatim(t *testing.T) {
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
					w.WriteHeader(http.StatusTeapot)
				}))
				defer server.Close()
				client := budgetAdapterClient(t, protocol, ProviderConfig{Model: "test-model", APIKey: "test-key", BaseURL: server.URL, ContextWindowTokens: 64000, MaxContextTokens: 64000, MaxOutputTokens: 128}, server.Client())
				history := strings.Repeat("optional_history ", 2000)
				_ = runBudgetAdapter(t, client, GenerateRequest{
					Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}},
					Messages: []Message{
						{Role: RoleSystem, Content: "system"},
						{Role: RoleUser, Content: history, Priority: MessagePriorityHistory},
						{Role: RoleAssistant, Priority: MessagePriorityHistory, ToolCalls: []ToolCall{
							{ID: "budget-call-a", Name: "lookup", Arguments: map[string]any{"query": "a"}},
							{ID: "budget-call-b", Name: "lookup", Arguments: map[string]any{"query": "b"}},
						}},
						{Role: RoleTool, Priority: MessagePriorityHistory, ToolCallID: "budget-call-a", ToolName: "lookup", Content: "first result"},
						{Role: RoleTool, Priority: MessagePriorityHistory, ToolCallID: "budget-call-b", ToolName: "lookup", Content: "second result"},
						{Role: RoleUser, Content: "CURRENT", Priority: MessagePriorityCurrent},
					},
				}, stream)
				var captured string
				select {
				case captured = <-bodies:
				default:
					t.Fatal("fitting request did not reach the provider")
				}
				for _, want := range []string{strings.TrimSpace(history), "budget-call-a", "budget-call-b", "first result", "second result", "CURRENT"} {
					if !strings.Contains(captured, want) {
						t.Fatalf("wire lost %.40q", want)
					}
				}
			})
		}
	}
}
