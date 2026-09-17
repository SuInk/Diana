package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func collectProviderEvents(t *testing.T, events <-chan ChatEvent) *GenerateResponse {
	t.Helper()
	result := &GenerateResponse{}
	for e := range events {
		if e.Type == ChatEventError {
			t.Fatal(e.Error)
		}
		result.Text += e.Text
		if e.ToolCall != nil {
			result.ToolCalls = append(result.ToolCalls, *e.ToolCall)
		}
		if e.Usage != nil {
			result.Usage = *e.Usage
		}
		if e.Response != nil {
			result.Provider = e.Response.Provider
			result.Model = e.Response.Model
			result.ReasoningContent = e.Response.ReasoningContent
			result.AnthropicThinking = e.Response.AnthropicThinking
			result.ResponsesOutput = e.Response.ResponsesOutput
		}
	}
	return result
}

func TestResponsesStreamPreservesCallIDAndEncryptedContinuation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Error("responses request is not streaming")
		}
		if calls == 2 {
			raw, _ := json.Marshal(body["input"])
			if !strings.Contains(string(raw), `"encrypted_content":"opaque"`) || !strings.Contains(string(raw), `"call_id":"call_real"`) {
				t.Errorf("continuation lost: %s", raw)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_item","call_id":"call_real","name":"lookup","arguments":""}}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"response.function_call_arguments.done","item_id":"fc_item","arguments":"{}"}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp","object":"response","model":"response-test","status":"completed","output":[{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"opaque"},{"type":"function_call","id":"fc_item","call_id":"call_real","name":"lookup","arguments":"{}","status":"completed"}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`+"\n\n")
	}))
	defer server.Close()
	registry, sel, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", BaseURL: server.URL, Model: "response-test", APIFormat: APIFormatResponses}))
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: sel}
	req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "lookup"}}, Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	first := collectProviderEvents(t, events)
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_real" || len(first.ResponsesOutput) != 2 {
		t.Fatalf("response=%+v", first)
	}
	req.Messages = append(req.Messages, Message{Role: RoleAssistant, ToolCalls: first.ToolCalls, ResponsesOutput: first.ResponsesOutput}, Message{Role: RoleTool, ToolCallID: first.ToolCalls[0].ID, ToolName: "lookup", Content: "done"})
	events, err = client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	collectProviderEvents(t, events)
	if calls != 2 {
		t.Fatal("missing continuation")
	}
}

func TestAnthropicStreamPreservesSignedThinkingAndUsage(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Error("anthropic request is not streaming")
		}
		if calls == 2 {
			raw, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(raw), `"signature":"signature-token"`) {
				t.Errorf("signed thinking lost: %s", raw)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":5,"output_tokens":0,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signature-token"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_a","name":"lookup","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"test\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		} {
			var e map[string]any
			json.Unmarshal([]byte(event), &e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], event)
		}
	}))
	defer server.Close()
	registry, sel, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{Provider: ProviderAnthropic, APIKey: "key", BaseURL: server.URL, Model: "claude-test"}))
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: sel}
	req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "lookup"}}, Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	first := collectProviderEvents(t, events)
	if first.Text != "" || first.Provider != ProviderAnthropic || first.Model != "claude-test" || len(first.ToolCalls) != 1 || first.Usage.InputTokens != 10 || first.Usage.TotalTokens != 14 || first.Usage.CachedInputTokens != 3 || len(first.AnthropicThinking) != 1 {
		t.Fatalf("response=%+v", first)
	}
	req.Messages = append(req.Messages, Message{Role: RoleAssistant, ToolCalls: first.ToolCalls, AnthropicThinking: first.AnthropicThinking}, Message{Role: RoleTool, ToolCallID: first.ToolCalls[0].ID, ToolName: "lookup", Content: "done"})
	events, err = client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	collectProviderEvents(t, events)
}

func TestGeminiStreamPreservesThoughtSignature(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("not streaming: %s", r.URL.Path)
		}
		if calls == 2 {
			raw, _ := json.Marshal(body)
			if !strings.Contains(string(raw), `"thoughtSignature":"c2ln"`) {
				t.Errorf("signature lost: %s", raw)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"candidates":[{"content":{"role":"model","parts":[{"text":"private reasoning","thought":true},{"functionCall":{"id":"tool_g","name":"lookup","args":{"q":"test"}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8,"cachedContentTokenCount":2}}`+"\n\n")
	}))
	defer server.Close()
	registry, sel, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{Provider: ProviderGemini, APIKey: "key", BaseURL: server.URL, Model: "gemini-test"}))
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: sel}
	req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "lookup"}}, Tools: []ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
	events, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	first := collectProviderEvents(t, events)
	if first.Text != "" || first.Model != "gemini-test" || len(first.ToolCalls) != 1 || string(first.ToolCalls[0].ThoughtSignature) != "sig" || first.Usage.TotalTokens != 8 {
		t.Fatalf("response=%+v", first)
	}
	req.Messages = append(req.Messages, Message{Role: RoleAssistant, ToolCalls: first.ToolCalls}, Message{Role: RoleTool, ToolCallID: first.ToolCalls[0].ID, ToolName: "lookup", Content: "done"})
	events, err = client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	collectProviderEvents(t, events)
}
