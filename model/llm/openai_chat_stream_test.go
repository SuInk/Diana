package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func writeChatEvents(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		fmt.Fprintf(w, "data: %s\n\n", event)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestChatStreamRejectsIncompleteCalls(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"invalid_json", `data: {broken}` + "\n\n"},
		{"early_eof", `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"},
		{"bad_args", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{\"x\":"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"},
		{"array_args", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"},
		{"length", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n"},
		{"late_error", `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{}"}}]}}]}` + "\n\n" + `data: {"error":{"message":"upstream failed"}}` + "\n\n"},
		{"reasoning_only", `data: {"choices":[{"index":0,"delta":{"reasoning_content":"private"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			done := false
			err := decodeChatCompletionEvents(context.Background(), strings.NewReader(tc.data), GenerateRequest{}, func(e ChatEvent) bool {
				if e.ToolCall != nil {
					calls++
				}
				done = done || e.Type == ChatEventDone
				return true
			})
			if err == nil || calls != 0 || done {
				t.Fatalf("err=%v calls=%d done=%v", err, calls, done)
			}
		})
	}
}

func TestChatStreamPreservesReasoningOnToolContinuation(t *testing.T) {
	var captured bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !body.Stream {
			t.Error("tool request must stream")
		}
		if len(body.Messages) == 3 {
			assistant := body.Messages[1]
			if assistant.ReasoningContent == nil || *assistant.ReasoningContent != "private state" || assistant.ToolCalls[0].ID != "call-1" || body.Messages[2].ToolCallID != "call-1" {
				t.Errorf("continuation lost: %+v", body.Messages)
			}
			captured = true
			writeChatEvents(w, `{"model":"mimo-test","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`)
			return
		}
		writeChatEvents(w,
			`{"model":"mimo-test","choices":[{"index":0,"delta":{"reasoning_content":"private ","reasoning":"private "}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"state","reasoning":"state","tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":7}}}`)
	}))
	defer server.Close()
	registry, selection, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test-key", Model: "mimo-test", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions}))
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: selection}
	req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "lookup"}}, Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var metadata *GenerateResponse
	var call *ToolCall
	var usage Usage
	for e := range events {
		if e.Type == ChatEventError {
			t.Fatal(e.Error)
		}
		if e.Text != "" {
			t.Fatal("reasoning leaked into text")
		}
		if e.ToolCall != nil {
			call = e.ToolCall
		}
		if e.Response != nil {
			metadata = e.Response
		}
		if e.Usage != nil {
			usage = *e.Usage
		}
	}
	if metadata == nil || metadata.Model != "mimo-test" || metadata.ReasoningContent == nil || *metadata.ReasoningContent != "private state" || call == nil || usage.CachedInputTokens != 7 {
		t.Fatalf("metadata=%+v call=%+v usage=%+v", metadata, call, usage)
	}
	req.Messages = append(req.Messages, Message{Role: RoleAssistant, ReasoningContent: metadata.ReasoningContent, ToolCalls: []ToolCall{*call}}, Message{Role: RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: "result"})
	events, err = client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for e := range events {
		if e.Type == ChatEventError {
			t.Fatal(e.Error)
		}
	}
	if !captured {
		t.Fatal("continuation was not sent")
	}
	// Reasoning must never be included in ordinary trace/history serialization.
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "private state") {
		t.Fatal("reasoning leaked into logs")
	}
}

func TestChatStreamIdleTimeoutAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, Model: "test", APIKey: "key", BaseURL: server.URL, APIFormat: APIFormatChatCompletions, Timeout: 100 * time.Millisecond}, server.Client())
			events, err := client.Stream(ctx, GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			if cancelled {
				cancel()
			}
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			failed := false
			for {
				select {
				case e, ok := <-events:
					if !ok {
						if !cancelled && !failed {
							t.Fatal("missing timeout error")
						}
						return
					}
					failed = failed || e.Type == ChatEventError
				case <-timer.C:
					t.Fatal("stream did not close")
				}
			}
		})
	}
}

func TestChatStreamRetriesStrictSchemaWithoutLeavingStreaming(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var body openAIChatCompletionRequest
		json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			t.Error("strict fallback stopped streaming")
		}
		if attempts == 1 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"strict schema is not supported"}}`)
			return
		}
		if body.Tools[0].Function.Strict {
			t.Error("strict was not removed")
		}
		writeChatEvents(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()
	client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", BaseURL: server.URL, Model: "test", APIFormat: APIFormatChatCompletions}, server.Client())
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}, Strict: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for e := range events {
		if e.Type == ChatEventError {
			t.Fatal(e.Error)
		}
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestMalformedToolArgumentsRemainErrorsInFallback(t *testing.T) {
	for _, raw := range []any{`{"unfinished":`, `[]`, `null`, 42.0} {
		payload := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "call", "function": map[string]any{"name": "lookup", "arguments": raw}}}}}}}
		if calls, err := openAIChatToolCallsFromPayload(payload, nil); err == nil || len(calls) > 0 {
			t.Fatalf("raw=%v calls=%+v err=%v", raw, calls, err)
		}
	}
}
