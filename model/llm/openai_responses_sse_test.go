// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Gateways may return SSE even when Generate requested a JSON response. A
// tool-only turn must remain executable, including its opaque continuation.
func TestResponsesGenerateSSEPreservesToolContinuation(t *testing.T) {
	for _, withText := range []bool{false, true} {
		t.Run(fmt.Sprintf("with_text=%t", withText), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls == 1 {
					items := `{"type":"reasoning","id":"rs_test","encrypted_content":"opaque-test-state","summary":[]},{"type":"function_call","id":"fc_test","call_id":"call_test","name":"read_file","arguments":"{\"path\":\"input.json\"}","status":"completed"}`
					if withText {
						items = `{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Reading the file.","annotations":[]}]},` + items
					}
					fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"test-model\",\"output\":[%s],\"usage\":{\"input_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":12},\"output_tokens\":5,\"total_tokens\":25}}}\n\n", items)
				} else {
					var input []map[string]any
					if err := json.Unmarshal(request["input"], &input); err != nil {
						t.Error(err)
						return
					}
					var reasoning, call, result bool
					for _, item := range input {
						switch item["type"] {
						case "reasoning":
							reasoning = item["encrypted_content"] == "opaque-test-state"
						case "function_call":
							call = item["call_id"] == "call_test"
						case "function_call_output":
							result = item["call_id"] == "call_test" && item["output"] == "file contents"
						}
					}
					if !reasoning || !call || !result {
						t.Errorf("continuation lost: %s", request["input"])
					}
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Done\"}]}]}}\n\n")
				}
			}))
			defer server.Close()
			c := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL + "/v1", APIFormat: APIFormatResponses, Model: "test-model"}, server.Client())
			tools := []ToolDefinition{{Name: "read_file", Description: "Read file", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
			messages := []Message{{Role: RoleUser, Content: "Read input.json"}}
			first, err := c.Generate(context.Background(), GenerateRequest{Messages: messages, Tools: tools})
			if err != nil {
				t.Fatal(err)
			}
			if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_test" || first.ToolCalls[0].Name != "read_file" || first.ToolCalls[0].Arguments["path"] != "input.json" {
				t.Fatalf("lost tool call: %#v", first)
			}
			if first.Usage.CachedInputTokens != 12 || first.Usage.TotalTokens != 25 {
				t.Fatalf("usage: %#v", first.Usage)
			}
			if withText && first.Text != "Reading the file." {
				t.Fatalf("text: %q", first.Text)
			}
			messages = append(messages, Message{Role: RoleAssistant, Content: first.Text, ToolCalls: first.ToolCalls, ResponsesOutput: first.ResponsesOutput, ContinuationScope: first.ContinuationScope}, Message{Role: RoleTool, Content: "file contents", ToolName: "read_file", ToolCallID: "call_test"})
			second, err := c.Generate(context.Background(), GenerateRequest{Messages: messages, Tools: tools})
			if err != nil {
				t.Fatal(err)
			}
			if second.Text != "Done" || second.Model != "test-model" {
				t.Fatalf("second: %#v", second)
			}
		})
	}
}

func TestResponsesSSEPreservesIncompleteStatus(t *testing.T) {
	stream := `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","id":"rs_test","summary":[]}]}}` + "\n\n"
	response, err, _ := decodeOpenAIResponseSSE(strings.NewReader(stream), "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if err = openAIResponsesEmptyOutputError(response); !errors.Is(err, ErrCompletionTruncatedNoText) {
		t.Fatalf("expected truncation, got %v", err)
	}
}

// 连接中途断开时，错误事件要带上原始错误，上层才能按类型认出连接故障去重试。
func TestResponsesStreamTransportErrorKeepsCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"半\"}\n\n")
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	c := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL + "/v1", APIFormat: APIFormatResponses, Model: "test-model"}, server.Client())
	events, err := c.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var failure *ChatEvent
	for event := range events {
		if event.Type == ChatEventError {
			failure = &event
		}
	}
	if failure == nil || failure.ErrorCause == nil || failure.Error != failure.ErrorCause.Error() {
		t.Fatalf("error event = %+v", failure)
	}
}
