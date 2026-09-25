// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failoverHistory 复现线上那一轮：第 1 步由 Gemini 调用 tools_load，第 2 步
// Gemini 限流、切到 DeepSeek 接着跑。gemini 这一段带的是别的作用域。
func failoverHistory(geminiScope string) []Message {
	return []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "你现在用的是什么模型"},
		{Role: RoleAssistant, ContinuationScope: geminiScope, ToolCalls: []ToolCall{{
			ID: "call_0", Name: "tools_load", Arguments: map[string]any{"names": []any{"runtime_model"}}, ThoughtSignature: []byte("gemini-signature"),
		}}},
		{Role: RoleTool, ToolCallID: "call_0", ToolName: "tools_load", Content: `{"loaded":["runtime_model"]}`},
	}
}

func TestDeepSeekFlattensForeignToolTurns(t *testing.T) {
	gemini := ProviderConfig{Provider: ProviderGemini, Model: "gemini-3.8-flash-low", BaseURL: "https://gemini.example"}
	geminiScope := continuationScope(gemini, "")
	for _, target := range []ProviderConfig{
		{Provider: ProviderOpenAICompatible, Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIFormat: APIFormatResponses},
		{Provider: ProviderOpenAICompatible, Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIFormat: APIFormatChatCompletions},
		// 经网关转发时端点不带 deepseek，靠模型名识别。
		{Provider: ProviderOpenAICompatible, Model: "deepseek-v4-pro", BaseURL: "https://gateway.example/v1", APIFormat: APIFormatChatCompletions},
	} {
		t.Run(string(target.APIFormat)+"/"+target.BaseURL, func(t *testing.T) {
			history := failoverHistory(geminiScope)
			got := (GenerateRequest{Messages: history}).withDefaults(target).Messages
			if len(got) != len(history) {
				t.Fatalf("消息条数变了：%d → %d", len(history), len(got))
			}
			call, result := got[2], got[3]
			if call.Role != RoleAssistant || len(call.ToolCalls) != 0 || !strings.Contains(call.Content, "tools_load") || !strings.Contains(call.Content, "runtime_model") {
				t.Fatalf("外来工具调用没有改写成文本：%+v", call)
			}
			if result.Role != RoleUser || result.ToolCallID != "" || !strings.Contains(result.Content, "tools_load") || !strings.Contains(result.Content, `"loaded"`) {
				t.Fatalf("外来工具结果没有改写成观察文本：%+v", result)
			}
			if len(history[2].ToolCalls) != 1 || history[3].Role != RoleTool {
				t.Fatal("调用方的历史被改动")
			}
		})
	}
}

func TestDeepSeekKeepsOwnToolTurnsNative(t *testing.T) {
	gemini := ProviderConfig{Provider: ProviderGemini, Model: "gemini-3.8-flash-low", BaseURL: "https://gemini.example"}
	deepseek := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", APIFormat: APIFormatChatCompletions}
	reasoning := "deepseek 自己的思考"
	history := append(failoverHistory(continuationScope(gemini, "")),
		// DeepSeek 自己的调用，ID 和前面 Gemini 的撞了。
		Message{Role: RoleAssistant, ContinuationScope: continuationScope(deepseek, ""), ReasoningContent: &reasoning, ToolCalls: []ToolCall{{ID: "call_0", Name: "runtime_model", Arguments: map[string]any{"group": "current"}}}},
		Message{Role: RoleTool, ToolCallID: "call_0", ToolName: "runtime_model", Content: "deepseek-flash"},
	)
	got := (GenerateRequest{Messages: history}).withDefaults(deepseek).Messages
	own, ownResult := got[4], got[5]
	if len(own.ToolCalls) != 1 || own.ReasoningContent == nil || *own.ReasoningContent != reasoning {
		t.Fatalf("本模型的原生调用被改写：%+v", own)
	}
	if ownResult.Role != RoleTool || ownResult.ToolCallID != "call_0" || ownResult.Content != "deepseek-flash" {
		t.Fatalf("本模型的工具结果被改写：%+v", ownResult)
	}
	raw, err := json.Marshal(openAIChatCompletionMessages(got, nil))
	if err != nil {
		t.Fatal(err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for i, msg := range wire {
		if _, hasCalls := msg["tool_calls"]; hasCalls && msg["reasoning_content"] == nil {
			t.Fatalf("第 %d 条带工具调用却没有 reasoning_content，DeepSeek 会 400：%s", i, raw)
		}
	}
}

func TestNonReasoningReplayTargetsKeepForeignToolCallsNative(t *testing.T) {
	gemini := ProviderConfig{Provider: ProviderGemini, Model: "gemini-3.8-flash-low", BaseURL: "https://gemini.example"}
	for _, target := range []ProviderConfig{
		{Provider: ProviderAnthropic, Model: "claude-sonnet-4-6", BaseURL: "https://api.anthropic.com"},
		{Provider: ProviderOpenAICompatible, Model: "gpt-5.5", BaseURL: "https://gateway.example/v1", APIFormat: APIFormatResponses},
	} {
		got := (GenerateRequest{Messages: failoverHistory(continuationScope(gemini, ""))}).withDefaults(target).Messages
		if len(got[2].ToolCalls) != 1 || got[3].Role != RoleTool || got[3].ToolCallID != "call_0" {
			t.Fatalf("%s 的外来工具调用不该被改写：%+v", target.Model, got)
		}
		if len(got[2].ToolCalls[0].ThoughtSignature) != 0 {
			t.Fatalf("%s 仍收到了外来签名", target.Model)
		}
	}
}

// TestDeepSeekResponsesRequestCarriesNoForeignFunctionCall 走真实 Generate，
// 钉住发到 DeepSeek Responses 接口的请求体里没有外来的 function_call。
func TestDeepSeekResponsesRequestCarriesNoForeignFunctionCall(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","created_at":1,"model":"deepseek-flash","output":[{"type":"message","id":"msg_test","status":"completed","role":"assistant","content":[{"type":"output_text","text":"deepseek-flash","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7},"status":"completed"}`))
	}))
	defer server.Close()

	gemini := ProviderConfig{Provider: ProviderGemini, Model: "gemini-3.8-flash-low", BaseURL: "https://gemini.example"}
	client, err := NewClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", Model: "deepseek-flash", BaseURL: server.URL + "/deepseek/v1", APIFormat: APIFormatResponses})
	if err != nil {
		t.Fatal(err)
	}
	tools := []ToolDefinition{{Name: "tools_load", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	if _, err := client.Generate(context.Background(), GenerateRequest{Messages: failoverHistory(continuationScope(gemini, "")), Tools: tools}); err != nil {
		t.Fatal(err)
	}
	var request struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("请求体解析失败：%v\n%s", err, body)
	}
	for _, item := range request.Input {
		if item["type"] == "function_call" || item["type"] == "function_call_output" {
			t.Fatalf("外来工具调用仍以原生条目发给 DeepSeek：%s", body)
		}
	}
	if !strings.Contains(body, "tools_load") || !strings.Contains(body, "runtime_model") {
		t.Fatalf("工具调用和结果从上下文里丢了：%s", body)
	}
}
