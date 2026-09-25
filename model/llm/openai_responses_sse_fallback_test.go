// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sub2api 转发 gpt-6-sol 时实测的事件形状：没要流式，回的却是事件流；模型只调工具、
// 不说话，整条流里没有一个文字增量。
const toolOnlyResponsesSSE = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","model":"gpt-6-sol","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"get_time","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"city\":\"北京\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":3,"output_index":0,"item_id":"fc_1","arguments":"{\"city\":\"北京\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"get_time","arguments":"{\"city\":\"北京\"}"}}

event: response.completed
data: {"type":"response.completed","sequence_number":5,"response":{"id":"resp_1","object":"response","model":"gpt-6-sol","status":"completed","output":%s,"usage":{"input_tokens":40,"input_tokens_details":{"cached_tokens":32},"output_tokens":9,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":49}}}

`

const toolOnlyCompletedOutput = `[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"get_time","arguments":"{\"city\":\"北京\"}"}]`

func generateAgainstResponsesSSE(t *testing.T, body string) (*GenerateResponse, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-6-sol",
	}, server.Client())
	return client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: RoleUser, Content: "北京现在几点？"}},
		Tools: []ToolDefinition{{
			Name:       "get_time",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}},
	})
}

// 线上现象：群聊主模型限流后切到 gpt-6-sol，每个带工具的回合都报
// 「event stream output is empty」。事件流兜底只拼文字，把 function_call 丢了。
func TestResponsesSSEFallbackKeepsToolOnlyOutput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
	}{
		{name: "completed 带完整 output", output: toolOnlyCompletedOutput},
		// 有的上游在 completed 里把 output 留空，只能靠 output_item.done 拼回来。
		{name: "completed 的 output 为空", output: `[]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(toolOnlyResponsesSSE, "%s", tt.output, 1)
			response, err := generateAgainstResponsesSSE(t, body)
			if err != nil {
				t.Fatalf("只调工具的回复被当成错误：%v", err)
			}
			if len(response.ToolCalls) != 1 {
				t.Fatalf("tool calls = %#v", response.ToolCalls)
			}
			call := response.ToolCalls[0]
			if call.ID != "call_1" || call.Name != "get_time" || call.Arguments["city"] != "北京" {
				t.Fatalf("tool call = %#v", call)
			}
			if response.Text != "" {
				t.Fatalf("text = %q, want empty", response.Text)
			}
			// 下一轮要把这个 function_call 原样续接回去，丢了它上游会报孤立的 tool output。
			if len(response.ResponsesOutput) != 1 {
				t.Fatalf("responses output = %d items, want 1", len(response.ResponsesOutput))
			}
			if response.Usage.TotalTokens != 49 || response.Usage.CachedInputTokens != 32 {
				t.Fatalf("usage = %#v", response.Usage)
			}
		})
	}
}

// 只有文字增量、没有 Responses 结构事件的网关照旧能拼出正文。
func TestResponsesSSEFallbackStillReadsBareTextDeltas(t *testing.T) {
	body := `data: {"type":"response.output_text.delta","delta":"你"}

data: {"type":"response.output_text.delta","delta":"好"}

data: [DONE]

`
	response, err := generateAgainstResponsesSSE(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "你好" || len(response.ToolCalls) != 0 {
		t.Fatalf("response = %#v", response)
	}
}

// 真的什么都没有时仍然报空，交给上层按空回复处理。
func TestResponsesSSEFallbackStillReportsEmptyStream(t *testing.T) {
	body := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}

`
	_, err := generateAgainstResponsesSSE(t, body)
	if err == nil || !strings.Contains(err.Error(), "event stream output is empty") {
		t.Fatalf("err = %v, want empty stream error", err)
	}
}
