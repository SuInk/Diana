package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAIResponsesInputMapsRoles 验证对应功能场景。
func TestOpenAIResponsesInputMapsRoles(t *testing.T) {
	got := openAIResponsesInput([]Message{
		{Role: RoleUser, Content: "user"},
		{Role: RoleAssistant, Content: "assistant"},
	})

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
	})

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
	})

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
	})

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
	if resp.Text != "hello from responses" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("response = %#v", resp)
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
		_, _ = w.Write([]byte(`{"model":"deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":"chat works"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIStyle: APIStyleChatCompletions,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "deepseek-v4-flash",
	}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{
		Messages:        []Message{{Role: RoleUser, Content: "hello"}},
		MaxOutputTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Fatalf("request body = %#v", gotBody)
	}
	if resp.Text != "chat works" || resp.Usage.TotalTokens != 5 {
		t.Fatalf("response = %#v", resp)
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
	if resp.Text != "chatstream" {
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
	for _, want := range []string{"403 Forbidden", "MODEL_DENIED", "permission_error", "model is not enabled"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error = %q, want substring %q", got, want)
		}
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
		if got := r.Header.Get("X-Relay"); got != "earlyso" {
			t.Fatalf("X-Relay = %q, want earlyso", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gp5.5","object":"model","owned_by":"earlyso"},{"id":"gpt-4o-mini"}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gp5.5",
		Headers:  map[string]string{"X-Relay": "earlyso"},
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/model" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(models) != 2 || models[0].ID != "gp5.5" || models[0].OwnedBy != "earlyso" {
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
		_, _ = w.Write([]byte(`{"models":["gp5.5","gpt-4o-mini"]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "gp5.5",
	}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/model" || paths[1] != "/v1/models" {
		t.Fatalf("paths = %#v", paths)
	}
	if len(models) != 2 || models[0].ID != "gp5.5" {
		t.Fatalf("models = %#v", models)
	}
}

// TestGeminiContentsMapsAssistantToModel 验证对应功能场景。
func TestGeminiContentsMapsAssistantToModel(t *testing.T) {
	got := geminiContents([]Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	})

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
	})

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
