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
	"sync"
	"testing"
	"time"
)

func freeformTestTools() []ToolDefinition {
	return []ToolDefinition{
		{Name: "codemode", Description: "run js", Parameters: map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}}, Freeform: &FreeformTool{Field: "code", Grammar: "start: SOURCE\nSOURCE: /[\\s\\S]+/", Hint: "raw source"}},
		{Name: "send", Parameters: map[string]any{"type": "object"}},
	}
}

func requestTools(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		out[name] = tool
	}
	return out
}

func inputTypes(body map[string]any) []string {
	var types []string
	items, _ := body["input"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if kind, _ := item["type"].(string); kind != "" && kind != "message" {
			types = append(types, kind)
		}
	}
	return types
}

// 带 Freeform 的工具按自定义工具发（带 Lark 文法），其余照旧是函数工具；模型回的
// custom_tool_call 还原成参数，第一行 @options 并进参数。
func TestResponsesSendsFreeformToolAndParsesCustomCall(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"codemode","input":"// @options: {\"fresh\":true}\nreturn 1 + 1;"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", APIFormat: APIFormatResponses, Model: "freeform-ok"}
	resp, err := newOpenAICompatibleClient(cfg, server.Client()).Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Tools: freeformTestTools()})
	if err != nil {
		t.Fatal(err)
	}
	tools := requestTools(t, body)
	if tools["codemode"]["type"] != "custom" || tools["send"]["type"] != "function" {
		t.Fatalf("tool types = %v / %v", tools["codemode"]["type"], tools["send"]["type"])
	}
	if format, _ := tools["codemode"]["format"].(map[string]any); format["syntax"] != "lark" || !strings.Contains(fmt.Sprint(format["definition"]), "SOURCE") {
		t.Fatalf("custom tool format = %#v", tools["codemode"]["format"])
	}
	if tools["codemode"]["description"] != "run js raw source" {
		t.Fatalf("custom tool description = %q", tools["codemode"]["description"])
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "codemode" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	if args := resp.ToolCalls[0].Arguments; args["code"] != "return 1 + 1;" || args["fresh"] != true {
		t.Fatalf("arguments = %#v", args)
	}
}

// 回放历史时调用和结果同型：这次按自定义工具发就都是 custom_tool_call(_output)；
// 降级成函数工具后，历史里原样保存的 custom_tool_call 也改写成 function_call。
func TestResponsesInputKeepsFreeformCallsAndOutputsPaired(t *testing.T) {
	rawCustom := json.RawMessage(`{"type":"custom_tool_call","id":"ctc_9","call_id":"call_9","name":"codemode","input":"return 9;"}`)
	history := []Message{
		{Role: RoleUser, Content: "q"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "codemode", Arguments: map[string]any{"code": "return 1;"}}}},
		{Role: RoleTool, ToolCallID: "call_1", ToolName: "codemode", Content: "1"},
		{Role: RoleAssistant, ResponsesOutput: []json.RawMessage{rawCustom}, ToolCalls: []ToolCall{{ID: "call_9", Name: "codemode", Arguments: map[string]any{"code": "return 9;"}}}},
		{Role: RoleTool, ToolCallID: "call_9", ToolName: "codemode", Content: "9"},
	}
	render := func(definitions []ToolDefinition) []map[string]any {
		raw, _ := json.Marshal(openAIResponsesInput(history, definitions))
		var items []map[string]any
		_ = json.Unmarshal(raw, &items)
		var out []map[string]any
		for _, item := range items {
			if item["type"] != nil && item["type"] != "message" {
				out = append(out, item)
			}
		}
		return out
	}
	freeform := render(freeformTestTools())
	want := []string{"custom_tool_call", "custom_tool_call_output", "custom_tool_call", "custom_tool_call_output"}
	for index, item := range freeform {
		if item["type"] != want[index] {
			t.Fatalf("freeform item %d = %v, want %s: %#v", index, item["type"], want[index], freeform)
		}
	}
	if freeform[0]["input"] != "return 1;" {
		t.Fatalf("custom call input = %#v", freeform[0])
	}

	stripped, _ := stripFreeformTools(GenerateRequest{Tools: freeformTestTools()})
	plain := render(stripped.Tools)
	want = []string{"function_call", "function_call_output", "function_call", "function_call_output"}
	for index, item := range plain {
		if item["type"] != want[index] {
			t.Fatalf("function item %d = %v, want %s: %#v", index, item["type"], want[index], plain)
		}
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(fmt.Sprint(plain[2]["arguments"])), &args)
	if args["code"] != "return 9;" {
		t.Fatalf("rewritten function_call arguments = %#v", plain[2]["arguments"])
	}
}

// 网关不认自定义工具（DeepSeek）：非流式和流式都去掉自定义工具重发一次，并记住结论，
// 下一次直接按函数工具发。
func TestResponsesFallsBackWhenCustomToolsRejected(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				if requestTools(t, body)["codemode"]["type"] == "custom" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"Unsupported custom tool: 'codemode'. Only 'apply_patch' is supported.","type":"invalid_request_error"}}`))
					return
				}
				item := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"codemode","arguments":"{\"code\":\"return 2;\"}","status":"completed"}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
					fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"m\",\"output\":[%s],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", item)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[%s],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, item)
			}))
			defer server.Close()
			now := time.Now()
			restore := stubDowngradeMemoClock(func() time.Time { return now })
			defer restore()
			model := fmt.Sprintf("no-custom-%t", stream)
			cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", APIFormat: APIFormatResponses, Model: model}
			request := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Tools: freeformTestTools()}
			run := func() []ToolCall {
				t.Helper()
				client := newOpenAICompatibleClient(cfg, server.Client())
				if !stream {
					resp, err := client.Generate(context.Background(), request)
					if err != nil {
						t.Fatal(err)
					}
					return resp.ToolCalls
				}
				events, err := client.Stream(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				var calls []ToolCall
				for event := range events {
					if event.Type == ChatEventError {
						t.Fatalf("stream error: %s", event.Error)
					}
					if event.ToolCall != nil {
						calls = append(calls, *event.ToolCall)
					}
				}
				return calls
			}
			calls := run()
			if len(calls) != 1 || calls[0].Arguments["code"] != "return 2;" {
				t.Fatalf("calls = %#v", calls)
			}
			mu.Lock()
			first := len(bodies)
			mu.Unlock()
			if first != 2 {
				t.Fatalf("requests = %d, want the rejection plus the function-tool retry", first)
			}
			run()
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 3 || requestTools(t, bodies[2])["codemode"]["type"] != "function" {
				t.Fatalf("the remembered fallback was not applied: requests=%d", len(bodies))
			}
		})
	}
}

func TestFreeformArgumentsRoundTrip(t *testing.T) {
	for _, input := range []string{"return 1;", "// @options: {\"fresh\":true}\nconst a = 1;\nreturn a;", "// 普通注释\nreturn 2;"} {
		args := freeformArguments("code", input)
		if back := freeformInput("code", args); back != input {
			t.Fatalf("round trip %q -> %#v -> %q", input, args, back)
		}
	}
	if args := freeformArguments("code", "// 普通注释\nreturn 2;"); args["code"] != "// 普通注释\nreturn 2;" || len(args) != 1 {
		t.Fatalf("an ordinary comment must stay in the code: %#v", args)
	}
}
