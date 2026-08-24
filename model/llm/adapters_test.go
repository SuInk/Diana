// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestOpenAIResponsesInputMapsRoles 验证对应功能场景。
func TestOpenAIResponsesInputMapsRoles(t *testing.T) {
	got := openAIResponsesInput([]Message{
		{Role: RoleUser, Content: "user"},
		{Role: RoleAssistant, Content: "assistant"},
	}, nil)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	for i, want := range []string{"user", "assistant"} {
		var wire struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		data, err := json.Marshal(got[i])
		if err != nil {
			t.Fatalf("Marshal(%d) error = %v", i, err)
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatalf("Unmarshal(%d) error = %v, json = %s", i, err, data)
		}
		if wire.Role != want || wire.Content == "" {
			t.Fatalf("message[%d] = %#v; json = %s", i, wire, data)
		}
	}
}

// TestOpenAIResponsesInputMapsImageParts 验证多模态图片会进入 Responses API input_image。
func TestOpenAIResponsesInputMapsImageParts(t *testing.T) {
	got := openAIResponsesInput([]Message{
		{
			Role:    RoleUser,
			Content: "这是什么",
			Parts: []ContentPart{
				{Type: ContentPartText, Text: "这是什么"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/image.jpg", Detail: "auto"},
			},
		},
	}, nil)

	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	var wire struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			ImageURL string `json:"image_url,omitempty"`
			Detail   string `json:"detail,omitempty"`
		} `json:"content"`
	}
	data, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("Unmarshal error = %v, json = %s", err, data)
	}
	if wire.Role != "user" || len(wire.Content) != 2 {
		t.Fatalf("message = %#v; json = %s", wire, data)
	}
	if wire.Content[0].Type != "input_text" || wire.Content[0].Text != "这是什么" {
		t.Fatalf("text content = %#v; json = %s", wire.Content[0], data)
	}
	if wire.Content[1].Type != "input_image" || wire.Content[1].ImageURL != "https://example.com/image.jpg" || wire.Content[1].Detail != "auto" {
		t.Fatalf("image content = %#v; json = %s", wire.Content[1], data)
	}
}

func TestAudioPartsReachMultimodalProviderPayloads(t *testing.T) {
	message := Message{Role: RoleUser, Content: "判断语气", Parts: []ContentPart{
		{Type: ContentPartText, Text: "判断语气"},
		{Type: ContentPartInputAudio, AudioData: "YXVkaW8=", AudioFormat: "wav"},
	}}

	responsesInput := openAIResponsesInput([]Message{message}, nil)
	if len(responsesInput) != 2 {
		t.Fatalf("responses input len=%d, want text message plus audio item", len(responsesInput))
	}
	responseAudio, err := json.Marshal(responsesInput[1])
	if err != nil || !strings.Contains(string(responseAudio), `"type":"input_audio"`) || !strings.Contains(string(responseAudio), `"data":"YXVkaW8="`) {
		t.Fatalf("responses audio=%s err=%v", responseAudio, err)
	}

	chatContent, ok := openAIChatCompletionContent(message).([]openAIChatCompletionContentPart)
	if !ok || len(chatContent) != 2 || chatContent[1].Type != "input_audio" || chatContent[1].InputAudio == nil || chatContent[1].InputAudio.Data != "YXVkaW8=" {
		t.Fatalf("chat content=%#v", chatContent)
	}

	gemini := geminiContents([]Message{message}, nil)
	if len(gemini) != 1 || len(gemini[0].Parts) != 2 || gemini[0].Parts[1].InlineData == nil || gemini[0].Parts[1].InlineData.MIMEType != "audio/wav" || string(gemini[0].Parts[1].InlineData.Data) != "audio" {
		t.Fatalf("gemini audio=%#v", gemini)
	}
}

func TestOpenAICompatibleGenerateImageUsesImageModel(t *testing.T) {
	var gotRequest struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
		N      int    `json:"n"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("Decode request error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"YWJjZA=="}]}`))
	}))
	defer server.Close()

	resp, err := GenerateImage(context.Background(), ProviderConfig{
		Provider:   ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	}, ImageGenerateRequest{Prompt: "画一只猫"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if gotRequest.Model != "gpt-image-2" || gotRequest.Prompt != "画一只猫" || gotRequest.Size != "1024x1024" || gotRequest.N != 1 {
		t.Fatalf("request = %#v", gotRequest)
	}
	if len(resp.Images) != 1 || resp.Images[0] != "data:image/png;base64,YWJjZA==" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestOpenAICompatibleEditImageUsesImageModelAndMultipart(t *testing.T) {
	var gotModel string
	var gotPrompt string
	var gotImage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("ParseMultipartForm error = %v", err)
		}
		gotModel = r.FormValue("model")
		gotPrompt = r.FormValue("prompt")
		file, _, err := r.FormFile("image")
		if err != nil {
			t.Fatalf("FormFile(image) error = %v", err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll(image) error = %v", err)
		}
		gotImage = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"ZWRpdA=="}]}`))
	}))
	defer server.Close()

	resp, err := EditImage(context.Background(), ProviderConfig{
		Provider:   ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	}, ImageEditRequest{
		Prompt: "把肤色变黑一点",
		Images: []string{"data:image/png;base64,aGVsbG8="},
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "gpt-image-2" || gotPrompt != "把肤色变黑一点" || gotImage != "hello" {
		t.Fatalf("model=%q prompt=%q image=%q", gotModel, gotPrompt, gotImage)
	}
	if len(resp.Images) != 1 || resp.Images[0] != "data:image/png;base64,ZWRpdA==" {
		t.Fatalf("response = %#v", resp)
	}
}

// TestGeminiContentsMapsImageParts 验证 Gemini 请求保留图片 URL 和 data URL。
func TestGeminiContentsMapsImageParts(t *testing.T) {
	got := geminiContents([]Message{
		{
			Role:    RoleUser,
			Content: "看图",
			Parts: []ContentPart{
				{Type: ContentPartText, Text: "看图"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/a.png"},
				{Type: ContentPartImageURL, ImageURL: "data:image/webp;base64,aGVsbG8="},
			},
		},
	}, nil)

	if len(got) != 1 || len(got[0].Parts) != 3 {
		t.Fatalf("contents = %#v", got)
	}
	if got[0].Parts[0].Text != "看图" {
		t.Fatalf("text part = %#v", got[0].Parts[0])
	}
	if got[0].Parts[1].FileData == nil || got[0].Parts[1].FileData.FileURI != "https://example.com/a.png" || got[0].Parts[1].FileData.MIMEType != "image/png" {
		t.Fatalf("file part = %#v", got[0].Parts[1])
	}
	if got[0].Parts[2].InlineData == nil || got[0].Parts[2].InlineData.MIMEType != "image/webp" || string(got[0].Parts[2].InlineData.Data) != "hello" {
		t.Fatalf("inline part = %#v", got[0].Parts[2])
	}
}

// TestAnthropicMessagesMapsImageParts 验证 Anthropic 请求保留图片 URL 和 base64 图片。
func TestAnthropicMessagesMapsImageParts(t *testing.T) {
	got := anthropicMessages([]Message{
		{
			Role:    RoleUser,
			Content: "看图",
			Parts: []ContentPart{
				{Type: ContentPartText, Text: "看图"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/a.jpg"},
				{Type: ContentPartImageURL, ImageURL: "data:image/png;base64,aGk="},
			},
		},
	}, nil)

	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	var wire struct {
		Role    string `json:"role"`
		Content []struct {
			Type   string `json:"type"`
			Text   string `json:"text,omitempty"`
			Source struct {
				Type      string `json:"type"`
				URL       string `json:"url,omitempty"`
				MediaType string `json:"media_type,omitempty"`
				Data      string `json:"data,omitempty"`
			} `json:"source,omitempty"`
		} `json:"content"`
	}
	data, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("Unmarshal error = %v, json = %s", err, data)
	}
	if wire.Role != "user" || len(wire.Content) != 3 {
		t.Fatalf("message = %#v; json = %s", wire, data)
	}
	if wire.Content[0].Type != "text" || wire.Content[0].Text != "看图" {
		t.Fatalf("text content = %#v; json = %s", wire.Content[0], data)
	}
	if wire.Content[1].Type != "image" || wire.Content[1].Source.Type != "url" || wire.Content[1].Source.URL != "https://example.com/a.jpg" {
		t.Fatalf("url image content = %#v; json = %s", wire.Content[1], data)
	}
	if wire.Content[2].Type != "image" || wire.Content[2].Source.Type != "base64" || wire.Content[2].Source.MediaType != "image/png" || wire.Content[2].Source.Data != "aGk=" {
		t.Fatalf("base64 image content = %#v; json = %s", wire.Content[2], data)
	}
}

// TestOpenAICompatibleDefaultsToResponsesAPI 验证对应功能场景。
func TestOpenAICompatibleDefaultsToResponsesAPI(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if gotPath != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", gotPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode request body error = %v", err)
		}
		if got := r.Header.Get("User-Agent"); got != DefaultOpenAICompatibleUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, DefaultOpenAICompatibleUserAgent)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","created_at":1,"model":"gpt-test","output":[{"type":"message","id":"msg_test","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello from responses","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7},"status":"completed"}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:        ProviderOpenAICompatible,
		APIKey:          "test-key",
		BaseURL:         server.URL + "/v1",
		Model:           "gpt-test",
		ReasoningEffort: "high",
		MaxOutputTokens: 256,
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["max_output_tokens"] != float64(256) || gotBody["messages"] != nil || gotBody["max_tokens"] != nil {
		t.Fatalf("request body = %#v", gotBody)
	}
	reasoning, ok := gotBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning = %#v; request body = %#v", gotBody["reasoning"], gotBody)
	}
	if resp.Text != "hello from responses" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestOpenAICompatibleResponsesNativeToolCall(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","created_at":1,"model":"gpt-test","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search.search","arguments":"{\"query\":\"Diana\"}","status":"completed"}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7},"status":"completed"}`))
	}))
	defer server.Close()
	client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL + "/v1", Model: "gpt-test"}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: RoleUser, Content: "search"}},
		Tools:    []ToolDefinition{{Name: "web_search.search", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Arguments["query"] != "Diana" {
		t.Fatalf("response=%#v", resp)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%#v body=%#v", tools, gotBody)
	}
}

func TestOpenAICompatibleGenerateLabelsLocalValidationErrors(t *testing.T) {
	client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible}, http.DefaultClient)
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "llm: local request validation failed: llm: missing model") {
		t.Fatalf("error = %v, want local validation label", err)
	}
}

func TestOpenAIResponsesInputPreservesNativeOutputItems(t *testing.T) {
	input := openAIResponsesInput([]Message{
		{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call_1", Name: "web_search.search", Arguments: map[string]any{"query": "Diana"}}},
			ResponsesOutput: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-state","summary":[]}`),
				json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search.search","arguments":"{\"query\":\"Diana\"}","status":"completed"}`),
			},
		},
		{Role: RoleTool, ToolCallID: "call_1", Content: `{"results":[]}`},
	}, nil)
	if len(input) != 3 {
		t.Fatalf("input len=%d, want reasoning + function_call + function_call_output", len(input))
	}
	reasoning, _ := json.Marshal(input[0])
	functionCall, _ := json.Marshal(input[1])
	functionOutput, _ := json.Marshal(input[2])
	if !strings.Contains(string(reasoning), `"type":"reasoning"`) || !strings.Contains(string(reasoning), `"encrypted_content":"opaque-state"`) {
		t.Fatalf("reasoning item was not preserved: %s", reasoning)
	}
	if !strings.Contains(string(functionCall), `"call_id":"call_1"`) || !strings.Contains(string(functionCall), `"name":"web_search.search"`) {
		t.Fatalf("function call item was not preserved: %s", functionCall)
	}
	if !strings.Contains(string(functionOutput), `"type":"function_call_output"`) || !strings.Contains(string(functionOutput), `"call_id":"call_1"`) {
		t.Fatalf("function output item was not appended: %s", functionOutput)
	}
}

func TestOpenAIResponsesInputRewritesItemIDOutputsToCallID(t *testing.T) {
	// 复现 miku 上的 400：结果引用了 item id（fc_…），而 function_call 的配对键是 call_id。
	input := openAIResponsesInput([]Message{
		{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "fc_1", Name: "web_search.search", Arguments: map[string]any{"query": "Diana"}}},
			ResponsesOutput: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search.search","arguments":"{}","status":"completed"}`),
			},
		},
		{Role: RoleTool, ToolCallID: "fc_1", Content: `{"results":[]}`},
	}, nil)
	if len(input) != 2 {
		t.Fatalf("input len=%d, want function_call + function_call_output", len(input))
	}
	functionOutput, _ := json.Marshal(input[1])
	if !strings.Contains(string(functionOutput), `"call_id":"call_1"`) {
		t.Fatalf("output call_id was not rewritten to the function call id: %s", functionOutput)
	}
}

func TestOpenAIResponsesInputPatchesMissingCallID(t *testing.T) {
	// 部分上游的 function_call 只有 id 没有 call_id；补上 call_id 才能和结果配对。
	input := openAIResponsesInput([]Message{
		{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "fc_1", Name: "web_search.search", Arguments: map[string]any{}}},
			ResponsesOutput: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","id":"fc_1","name":"web_search.search","arguments":"{}","status":"completed"}`),
			},
		},
		{Role: RoleTool, ToolCallID: "fc_1", Content: `{"results":[]}`},
	}, nil)
	if len(input) != 2 {
		t.Fatalf("input len=%d, want function_call + function_call_output", len(input))
	}
	functionCall, _ := json.Marshal(input[0])
	functionOutput, _ := json.Marshal(input[1])
	if !strings.Contains(string(functionCall), `"call_id":"fc_1"`) {
		t.Fatalf("function call was not patched with call_id: %s", functionCall)
	}
	if !strings.Contains(string(functionOutput), `"call_id":"fc_1"`) {
		t.Fatalf("output call_id changed unexpectedly: %s", functionOutput)
	}
}

func TestOpenAIResponsesInputDropsUnpairedFunctionItems(t *testing.T) {
	// 上下文裁剪可能剪掉配对的一半：孤儿结果和无结果的调用都不能进请求。
	input := openAIResponsesInput([]Message{
		{Role: RoleUser, Content: "hi"},
		{
			Role:      RoleAssistant,
			Content:   "先搜一下",
			ToolCalls: []ToolCall{{ID: "call_kept", Name: "web_search.search", Arguments: map[string]any{}}},
			ResponsesOutput: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","id":"fc_kept","call_id":"call_kept","name":"web_search.search","arguments":"{}","status":"completed"}`),
				json.RawMessage(`{"type":"function_call","id":"fc_dropped","call_id":"call_dropped","name":"web_search.search","arguments":"{}","status":"completed"}`),
			},
		},
		{Role: RoleTool, ToolCallID: "call_kept", Content: `{"results":[]}`},
		{Role: RoleTool, ToolCallID: "call_orphan", Content: `{"results":[]}`},
	}, nil)
	encoded, _ := json.Marshal(input)
	if strings.Contains(string(encoded), "call_dropped") {
		t.Fatalf("function call without output was not dropped: %s", encoded)
	}
	if strings.Contains(string(encoded), "call_orphan") {
		t.Fatalf("orphan function output was not dropped: %s", encoded)
	}
	if !strings.Contains(string(encoded), "call_kept") {
		t.Fatalf("paired function call should be kept: %s", encoded)
	}
}

func TestOpenAICompatibleResponsesContinuesAfterTextlessToolCall(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_final","object":"response","created_at":1,"model":"gpt-test","output":[{"type":"message","id":"msg_final","status":"completed","role":"assistant","content":[{"type":"output_text","text":"能力查询完成","annotations":[]}]}],"status":"completed"}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{
		{Role: RoleUser, Content: "群聊 Agent 有哪些能力"},
		{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call_1", Name: "diana.capabilities", Arguments: map[string]any{"query": "群聊 Agent"}}},
			ResponsesOutput: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"diana.capabilities","arguments":"{\"query\":\"群聊 Agent\"}","status":"completed"}`),
			},
		},
		{Role: RoleTool, ToolCallID: "call_1", ToolName: "diana.capabilities", Content: `{"results":[]}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "能力查询完成" {
		t.Fatalf("response = %#v", resp)
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("input = %#v", gotBody["input"])
	}
	functionCall, _ := input[1].(map[string]any)
	functionOutput, _ := input[2].(map[string]any)
	if functionCall["type"] != "function_call" || functionOutput["type"] != "function_call_output" {
		t.Fatalf("continuation input = %#v", input)
	}
}

func TestNativeToolMappingsForAnthropicAndGemini(t *testing.T) {
	definition := ToolDefinition{
		Name: "diana.relationship", Description: "query relationship", Strict: true,
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"operation": map[string]any{"type": "string"}}},
	}
	if tools := anthropicTools([]ToolDefinition{definition}); len(tools) != 1 || tools[0].OfTool == nil || tools[0].OfTool.Name != wireToolName(definition.Name) {
		t.Fatalf("anthropic tools=%#v", tools)
	}
	if tools := geminiTools([]ToolDefinition{definition}); len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 || tools[0].FunctionDeclarations[0].Name != wireToolName(definition.Name) {
		t.Fatalf("gemini tools=%#v", tools)
	}
	messages := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: definition.Name, Arguments: map[string]any{"operation": "get"}}}},
		{Role: RoleTool, ToolCallID: "call-1", ToolName: definition.Name, Content: `{"favorability":10}`},
	}
	if converted := anthropicMessages(messages, []ToolDefinition{definition}); len(converted) != 2 {
		t.Fatalf("anthropic messages=%#v", converted)
	}
	if converted := geminiContents(messages, []ToolDefinition{definition}); len(converted) != 2 || converted[0].Parts[0].FunctionCall.ID != "call-1" || converted[1].Parts[0].FunctionResponse.ID != "call-1" {
		t.Fatalf("gemini messages=%#v", converted)
	}
}

func TestOpenAICompatibleResponsesOmitsUnconfiguredMaxOutputTokens(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode request body error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","created_at":1,"model":"gpt-test","output":[{"type":"message","id":"msg_test","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"status":"completed"}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	if _, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if _, exists := gotBody["max_output_tokens"]; exists {
		t.Fatalf("max_output_tokens should be omitted when configured as 0: %#v", gotBody)
	}
}

func TestOpenAICompatibleChatCompletionsAPI(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode request body error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_test","model":"deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":"chat completion ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:        ProviderOpenAICompatible,
		APIKey:          "test-key",
		BaseURL:         server.URL + "/v1",
		APIFormat:       APIFormatChatCompletions,
		Model:           "deepseek-v4-flash",
		ReasoningEffort: "high",
		MaxOutputTokens: 256,
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{
		{Role: RoleSystem, Content: "system prompt"},
		{
			Role:    RoleUser,
			Content: "看图",
			Parts: []ContentPart{
				{Type: ContentPartText, Text: "看图"},
				{Type: ContentPartImageURL, ImageURL: "https://example.com/image.jpg", Detail: "high"},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "deepseek-v4-flash" || gotBody["max_tokens"] != float64(256) || gotBody["reasoning_effort"] != "high" || gotBody["stream"] != true {
		t.Fatalf("request body = %#v", gotBody)
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	systemMessage, _ := messages[0].(map[string]any)
	if systemMessage["role"] != "system" || systemMessage["content"] != "system prompt" {
		t.Fatalf("system message = %#v", systemMessage)
	}
	userMessage, _ := messages[1].(map[string]any)
	parts, ok := userMessage["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user message = %#v", userMessage)
	}
	imagePart, _ := parts[1].(map[string]any)
	imageURL, _ := imagePart["image_url"].(map[string]any)
	if imagePart["type"] != "image_url" || imageURL["url"] != "https://example.com/image.jpg" || imageURL["detail"] != "high" {
		t.Fatalf("image part = %#v", imagePart)
	}
	if resp.Model != "deepseek-v4-flash" || resp.Text != "chat completion ok" || resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 4 || resp.Usage.TotalTokens != 13 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestOpenAICompatibleChatCompletionsParsesContentArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"array-model",
			"choices":[{
				"message":{"role":"assistant","content":[
					{"type":"text","text":"array "},
					{"type":"output_text","text":{"value":"content"}},
					{"type":"reasoning","text":"internal analysis"}
				]},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "array-model",
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "array content" || resp.Usage.TotalTokens != 5 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestOpenAICompatibleChatCompletionsSSEUsesFinalMessageWithoutDuplication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"internal analysis"}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":[{"type":"text","text":"stream "}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte("event: done\n"))
		_, _ = w.Write([]byte(`data: {"done":true,"message":{"role":"assistant","content":[{"type":"text","text":"stream content"}]},"finish_reason":"stop","usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "stream-model",
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "stream content" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestOpenAICompatibleEventStreamMergesFinalFragmentsAndSnapshots(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "message fragments",
			body: `data: {"choices":[{"message":{"content":"hello "}}]}

data: {"choices":[{"message":{"content":"world"},"finish_reason":"stop"}]}

data: [DONE]

`,
			want: "hello world",
		},
		{
			name: "message snapshot growth",
			body: `data: {"choices":[{"message":{"content":"hello"}}]}

event: done
data: {"done":true,"message":{"content":"hello world"},"finish_reason":"stop"}

data: [DONE]

`,
			want: "hello world",
		},
		{
			name: "repeated message fragments",
			body: `data: {"choices":[{"message":{"content":"哈"}}]}

data: {"choices":[{"message":{"content":"哈"},"finish_reason":"stop"}]}

data: [DONE]

`,
			want: "哈哈",
		},
		{
			name: "prefix message fragments",
			body: `data: {"choices":[{"message":{"content":"a"}}]}

data: {"choices":[{"message":{"content":"ab"},"finish_reason":"stop"}]}

data: [DONE]

`,
			want: "aab",
		},
		{
			name: "output text done fragments",
			body: `event: response.output_text.done
data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"hello "}

event: response.output_text.done
data: {"type":"response.output_text.done","output_index":0,"content_index":1,"text":"world"}

data: [DONE]

`,
			want: "hello world",
		},
		{
			name: "output text done snapshot growth",
			body: `event: response.output_text.done
data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"hello"}

event: response.output_text.done
data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"hello world"}

data: [DONE]

`,
			want: "hello world",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _, err := decodeOpenAITextEventStream(strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if text != tt.want {
				t.Fatalf("text = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestOpenAICompatibleEventStreamUsesCompletedResponseOutput(t *testing.T) {
	body := `event: response.completed
data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"completed output"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}

data: [DONE]

`
	text, usage, err := decodeOpenAITextEventStream(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if text != "completed output" || usage.TotalTokens != 5 {
		t.Fatalf("text=%q usage=%#v", text, usage)
	}
}

func TestOpenAICompatibleEventStreamDoesNotExposeTypedReasoningEvents(t *testing.T) {
	body := `event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","delta":"private reasoning"}

event: response.reasoning_text.delta
data: {"type":"response.reasoning_text.delta","delta":"more private reasoning"}

data: [DONE]

`
	result, err := decodeOpenAITextEventStreamResult(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" || !result.Diagnostics.ReasoningSeen {
		t.Fatalf("result = %#v, want empty text with reasoning_seen", result)
	}
}

func TestOpenAICompatibleChatCompletionsEmptyOutputHasSanitizedDiagnostics(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name:        "json",
			contentType: "application/json",
			body:        `{"choices":[{"message":{"content":"","reasoning_content":"private chain of thought"},"finish_reason":"length"}],"usage":{"prompt_tokens":8,"completion_tokens":16,"total_tokens":24}}`,
		},
		{
			name:        "sse",
			contentType: "text/event-stream",
			body: `data: {"choices":[{"delta":{"reasoning_content":"private chain of thought"}}]}

data: {"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":8,"completion_tokens":16,"total_tokens":24}}

data: [DONE]

`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := newOpenAICompatibleClient(ProviderConfig{
				Provider:  ProviderOpenAICompatible,
				APIKey:    "test-key",
				BaseURL:   server.URL + "/v1",
				APIFormat: APIFormatChatCompletions,
				Model:     "empty-model",
			}, server.Client())
			_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
			if err == nil {
				t.Fatal("Generate error = nil, want empty output error")
			}
			if !errors.Is(err, ErrCompletionTruncatedNoText) {
				t.Fatalf("error = %v, want ErrCompletionTruncatedNoText", err)
			}
			got := err.Error()
			for _, want := range []string{
				"output is empty",
				"finish_reason=length",
				"reasoning_seen=true",
				"input_tokens:8",
				"output_tokens:16",
				"total_tokens:24",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("error = %q, want substring %q", got, want)
				}
			}
			if strings.Contains(got, "private chain of thought") {
				t.Fatalf("error leaked reasoning body: %q", got)
			}
		})
	}
}

func TestOpenAICompatibleChatCompletionsTerminalNoTextOutcomesAreNotEmptyErrors(t *testing.T) {
	const privatePayload = "private refusal or tool payload"
	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{
			name:        "content filter",
			contentType: "application/json",
			body:        `{"choices":[{"message":{"content":""},"finish_reason":"content_filter"}],"usage":{"total_tokens":6}}`,
			want:        "finish_reason=content_filter",
		},
		{
			name:        "refusal",
			contentType: "application/json",
			body:        `{"choices":[{"message":{"content":"","refusal":"` + privatePayload + `"},"finish_reason":"stop"}],"usage":{"total_tokens":6}}`,
			want:        "refusal_seen=true",
		},
		{
			name:        "tool calls",
			contentType: "text/event-stream",
			body: `data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"lookup","arguments":"` + privatePayload + `"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"total_tokens":6}}

data: [DONE]

`,
			want: "tool_calls_seen=true",
		},
		{
			name:        "function call",
			contentType: "text/event-stream",
			body: `data: {"choices":[{"delta":{"function_call":{"name":"lookup","arguments":"` + privatePayload + `"}}}]}

data: {"choices":[{"delta":{},"finish_reason":"function_call"}],"usage":{"total_tokens":6}}

data: [DONE]

`,
			want: "function_call_seen=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := newOpenAICompatibleClient(ProviderConfig{
				Provider:  ProviderOpenAICompatible,
				APIKey:    "test-key",
				BaseURL:   server.URL + "/v1",
				APIFormat: APIFormatChatCompletions,
				Model:     "no-text-model",
			}, server.Client())
			_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
			if !errors.Is(err, ErrCompletionHasNoText) {
				t.Fatalf("error = %v, want ErrCompletionHasNoText", err)
			}
			got := err.Error()
			if strings.Contains(got, "output is empty") {
				t.Fatalf("terminal outcome used transient marker: %q", got)
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("error = %q, want substring %q", got, tt.want)
			}
			if strings.Contains(got, privatePayload) {
				t.Fatalf("error leaked private payload: %q", got)
			}
		})
	}
}

func TestOpenAICompatibleChatCompletionsGenuinelyEmptyStopRemainsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "empty-model",
	}, server.Client())
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "output is empty") {
		t.Fatalf("error = %v, want transient empty output", err)
	}
	if errors.Is(err, ErrCompletionHasNoText) || errors.Is(err, ErrCompletionTruncatedNoText) {
		t.Fatalf("generic empty error had terminal classification: %v", err)
	}
}

func TestOpenAICompatibleChatCompletionsStreamActivityCanExceedConfiguredTimeout(t *testing.T) {
	const timeout = 120 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		chunks := []string{"持", "续", "流", "式", "输", "出"}
		for index, chunk := range chunks {
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"` + chunk + `"}}]}` + "\n\n"))
			flusher.Flush()
			if index < len(chunks)-1 {
				time.Sleep(35 * time.Millisecond)
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "stream-model",
		Timeout:   timeout,
	}, server.Client())
	started := time.Now()
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed <= timeout {
		t.Fatalf("stream completed in %s, want total duration beyond %s", elapsed, timeout)
	}
	if resp.Text != "持续流式输出" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestOpenAICompatibleChatCompletionsStopsOnStreamIdleTimeout(t *testing.T) {
	const timeout = 60 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"开始"}}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "stream-model",
		Timeout:   timeout,
	}, server.Client())
	started := time.Now()
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("error = %v, want stream idle timeout", err)
	}
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("idle timeout took %s", elapsed)
	}
}

func TestOpenAICompatibleChatCompletionsStopsAtDoneWithoutWaitingForConnectionClose(t *testing.T) {
	const timeout = 80 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"完成"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "stream-model",
		Timeout:   timeout,
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "完成" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestOpenAICompatibleChatCompletionsStopsWaitingForResponseHeaders(t *testing.T) {
	const timeout = 50 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "stream-model",
		Timeout:   timeout,
	}, server.Client())
	started := time.Now()
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "response header timeout") {
		t.Fatalf("error = %v, want response header timeout", err)
	}
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("response header timeout took %s", elapsed)
	}
}

// TestOpenAICompatibleResponsesAPIAcceptsEventStream 验证部分聚合商非流式请求返回 SSE 时仍能解析文本。
func TestOpenAICompatibleResponsesAPIAcceptsEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"群聊"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"回复正常"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "群聊回复正常" || resp.Usage.TotalTokens != 11 {
		t.Fatalf("response = %#v", resp)
	}
}

// TestOpenAICompatibleResponsesAPIAcceptsChatCompletionEventStream 验证兼容 Chat Completions 风格 SSE。
func TestOpenAICompatibleResponsesAPIAcceptsChatCompletionEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"chat "}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"stream"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "chat stream" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

// TestOpenAICompatibleResponsesAPIErrorIncludesRootJSONBody 验证对应功能场景。
func TestOpenAICompatibleResponsesAPIErrorIncludesRootJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"MODEL_DENIED","type":"permission_error","message":"model is not enabled for this key"}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err == nil {
		t.Fatal("Generate error = nil, want forbidden error")
	}
	got := err.Error()
	for _, want := range []string{"llm: provider request failed", "403 Forbidden", "MODEL_DENIED", "permission_error", "model is not enabled"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error = %q, want substring %q", got, want)
		}
	}
}

func TestOpenAICompatibleResponsesAPIUnsupportedEndpointHasCompatibilityHint(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"unknown endpoint"}}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}})
	if err == nil {
		t.Fatal("Generate error = nil, want unsupported endpoint error")
	}
	got := err.Error()
	for _, want := range []string{"404 Not Found", "Responses API", "select Chat Completions", "/chat/completions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error = %q, want substring %q", got, want)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests=%d, want exactly 1 without protocol fallback", got)
	}
}

func TestOpenAICompatibleResponsesAPIDoesNotRetryInsideSDK(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"detail":"temporary upstream failure"}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gpt-test",
	}, server.Client())
	if _, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}}); err == nil {
		t.Fatal("Generate error = nil, want bad gateway error")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests=%d, want exactly 1", got)
	}
}

// TestListOpenAICompatibleModelsUsesModelEndpoint 验证对应功能场景。
func TestListOpenAICompatibleModelsUsesModelEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if gotPath != "/v1/model" {
			t.Fatalf("path = %q, want /v1/model", gotPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != DefaultOpenAICompatibleUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, DefaultOpenAICompatibleUserAgent)
		}
		if got := r.Header.Get("X-Relay"); got != "example-relay" {
			t.Fatalf("X-Relay = %q, want example-relay", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"example-chat-model","object":"model","owned_by":"example-relay"},{"id":"gpt-4o-mini"}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "example-chat-model",
		Headers:  map[string]string{"X-Relay": "example-relay"},
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/model" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(models) != 2 || models[0].ID != "example-chat-model" || models[0].OwnedBy != "example-relay" {
		t.Fatalf("models = %#v", models)
	}
}

// TestListOpenAICompatibleModelsFallsBackToModelsEndpoint 验证对应功能场景。
func TestListOpenAICompatibleModelsFallsBackToModelsEndpoint(t *testing.T) {
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/model" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
			return
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":["example-chat-model","gpt-4o-mini"]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "example-chat-model",
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/model" || paths[1] != "/v1/models" {
		t.Fatalf("paths = %#v", paths)
	}
	if len(models) != 2 || models[0].ID != "example-chat-model" {
		t.Fatalf("models = %#v", models)
	}
}

// TestGeminiContentsMapsAssistantToModel 验证对应功能场景。
func TestGeminiContentsMapsAssistantToModel(t *testing.T) {
	got := geminiContents([]Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}, nil)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Role != "user" {
		t.Fatalf("first role = %q, want user", got[0].Role)
	}
	if got[1].Role != "model" {
		t.Fatalf("second role = %q, want model", got[1].Role)
	}
}

// TestAnthropicMessagesMapsRoles 验证对应功能场景。
func TestAnthropicMessagesMapsRoles(t *testing.T) {
	got := anthropicMessages([]Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}, nil)

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Role != "user" {
		t.Fatalf("system fallback role = %q, want user", got[0].Role)
	}
	if got[1].Role != "user" {
		t.Fatalf("user role = %q, want user", got[1].Role)
	}
	if got[2].Role != "assistant" {
		t.Fatalf("assistant role = %q, want assistant", got[2].Role)
	}
}
func TestForcedToolChoiceReachesEveryProvider(t *testing.T) {
	definition := ToolDefinition{Name: "agent.finalize", Description: "finish", Parameters: map[string]any{"type": "object"}}
	req := GenerateRequest{Tools: []ToolDefinition{definition}, ToolChoice: definition.Name}

	choice := anthropicToolChoice(req)
	if choice.OfTool == nil || choice.OfTool.Name != wireToolName(definition.Name) {
		t.Fatalf("anthropic tool choice=%#v", choice)
	}
	if auto := anthropicToolChoice(GenerateRequest{Tools: req.Tools}); auto.OfAuto == nil {
		t.Fatalf("anthropic default tool choice=%#v", auto)
	}

	config := geminiToolConfig(req)
	if config == nil || config.FunctionCallingConfig == nil ||
		config.FunctionCallingConfig.Mode != "ANY" ||
		len(config.FunctionCallingConfig.AllowedFunctionNames) != 1 ||
		config.FunctionCallingConfig.AllowedFunctionNames[0] != wireToolName(definition.Name) {
		t.Fatalf("gemini tool config=%#v", config)
	}
	if auto := geminiToolConfig(GenerateRequest{Tools: req.Tools}); auto != nil {
		t.Fatalf("gemini default tool config=%#v", auto)
	}

	responseChoice := openAIResponseToolChoice(req)
	if responseChoice.OfFunctionTool == nil || responseChoice.OfFunctionTool.Name != wireToolName(definition.Name) {
		t.Fatalf("responses tool choice=%#v", responseChoice)
	}
	chatChoice, _ := openAIChatToolChoice(req).(map[string]any)
	function, _ := chatChoice["function"].(map[string]any)
	if chatChoice["type"] != "function" || function["name"] != wireToolName(definition.Name) {
		t.Fatalf("chat tool choice=%#v", chatChoice)
	}
	if auto := openAIChatToolChoice(GenerateRequest{Tools: req.Tools}); auto != nil {
		t.Fatalf("chat default tool choice=%#v", auto)
	}
}

func TestAnthropicCachesStablePrefix(t *testing.T) {
	tools := anthropicTools([]ToolDefinition{
		{Name: "lookup", Parameters: map[string]any{"type": "object"}},
		{Name: "agent.finalize", Parameters: map[string]any{"type": "object"}},
	})
	if len(tools) != 2 {
		t.Fatalf("tools=%#v", tools)
	}
	if tools[1].OfTool.CacheControl.Type != "ephemeral" {
		t.Fatalf("last tool must carry the cache breakpoint: %#v", tools[1].OfTool.CacheControl)
	}
	if tools[0].OfTool.CacheControl.Type == "ephemeral" {
		t.Fatalf("cache breakpoint must sit on the last tool only: %#v", tools[0].OfTool.CacheControl)
	}

	converted := anthropicMessages([]Message{
		{Role: RoleUser, Content: "第一轮"},
		{Role: RoleAssistant, Content: "第一轮回复", CacheBreakpoint: true},
		{Role: RoleUser, Content: "第二轮"},
	}, nil)
	if len(converted) != 3 {
		t.Fatalf("messages=%#v", converted)
	}
	if control := converted[1].Content[0].GetCacheControl(); control == nil || control.Type != "ephemeral" {
		t.Fatalf("marked message cache control=%#v", converted[1].Content[0])
	}
	if control := converted[2].Content[0].GetCacheControl(); control != nil && control.Type == "ephemeral" {
		t.Fatalf("unmarked message must not be cached: %#v", converted[2].Content[0])
	}
}

func TestStrictToolSchemaRewritesOptionalArguments(t *testing.T) {
	plain := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content":    map[string]any{"type": "string"},
			"task_state": map[string]any{"type": "string", "enum": []string{"pending"}},
			"claims": map[string]any{"type": "array", "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string"},
					"summary": map[string]any{"type": "string"},
				},
				"required":             []string{"id"},
				"additionalProperties": false,
			}},
		},
		"required":             []string{"claims"},
		"additionalProperties": false,
	}
	strict := strictToolSchema(plain)

	if names := schemaRequiredNames(strict); len(names) != 3 {
		t.Fatalf("strict schema must require every property: %#v", names)
	}
	properties, _ := strict["properties"].(map[string]any)
	content, _ := properties["content"].(map[string]any)
	types, _ := content["type"].([]any)
	if len(types) != 2 || types[0] != "string" || types[1] != "null" {
		t.Fatalf("optional property was not widened: %#v", content)
	}
	taskState, _ := properties["task_state"].(map[string]any)
	enum, _ := taskState["enum"].([]any)
	if len(enum) != 2 || enum[0] != "pending" || enum[1] != nil {
		t.Fatalf("widened enum must admit null: %#v", taskState)
	}
	// Nested item schemas are rewritten too, otherwise the array elements stay
	// outside the grammar.
	claims, _ := properties["claims"].(map[string]any)
	items, _ := claims["items"].(map[string]any)
	if names := schemaRequiredNames(items); len(names) != 2 {
		t.Fatalf("array items were not rewritten: %#v", items)
	}
	// The caller's schema must not be mutated: other providers keep seeing the
	// genuinely optional form.
	if names := schemaRequiredNames(plain); len(names) != 1 {
		t.Fatalf("original schema was mutated: %#v", plain)
	}
}

func TestOpenAICompatibleRetriesOnceWhenGatewayRejectsStrictTools(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode request body error = %v", err)
		}
		requests = append(requests, body)
		tools, _ := body["tools"].([]any)
		first, _ := tools[0].(map[string]any)
		function, _ := first["function"].(map[string]any)
		if function["strict"] == true {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid schema: strict is not supported","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_test","model":"test","choices":[{"message":{"role":"assistant","content":"降级后仍然可用"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		APIFormat: APIFormatChatCompletions,
		Model:     "test",
	}, server.Client())
	request := GenerateRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []ToolDefinition{{
			Name:   "agent.finalize",
			Strict: true,
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"content": map[string]any{"type": "string"}},
				"additionalProperties": false,
			},
		}},
	}
	resp, err := client.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "降级后仍然可用" || len(requests) != 2 {
		t.Fatalf("response=%#v requests=%d", resp, len(requests))
	}

	// The downgrade is remembered, so the next turn does not pay for the
	// rejected attempt again.
	if _, err := client.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("strict downgrade was not remembered: requests=%d", len(requests))
	}
	last, _ := requests[2]["tools"].([]any)
	lastTool, _ := last[0].(map[string]any)
	lastFunction, _ := lastTool["function"].(map[string]any)
	if lastFunction["strict"] == true {
		t.Fatalf("strict was sent again after the downgrade: %#v", lastFunction)
	}
}
