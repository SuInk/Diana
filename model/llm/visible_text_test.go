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

func TestVisibleAssistantText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"<think>private reasoning</think>最终答复", "最终答复"},
		{"<think>İ Σ你好</think>答复", "答复"},
		{" \n<THINK>private</THINK>\n答复", "\n答复"},
		{"<thinking>private</thinking><think>more private</think>答复", "答复"},
		{"<think>unfinished private reasoning", ""},
		{"<think>private</think>", ""},
		{"<thin", ""},
		{"普通回答", "普通回答"},
		{"标签 <think>示例</think> 是文本", "标签 <think>示例</think> 是文本"},
		{"```xml\n<think>示例</think>\n```", "```xml\n<think>示例</think>\n```"},
		{"&lt;think&gt;示例", "&lt;think&gt;示例"},
		{"<thinker>不是思考标签", "<thinker>不是思考标签"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := VisibleAssistantText(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			for split := 0; split <= len(tc.in); split++ {
				var f VisibleTextFilter
				got := f.Push(tc.in[:split]) + f.Push(tc.in[split:]) + f.Finish()
				if got != tc.want {
					t.Fatalf("split=%d got %q want %q", split, got, tc.want)
				}
			}
			var f VisibleTextFilter
			var b strings.Builder
			for i := range len(tc.in) {
				b.WriteString(f.Push(tc.in[i : i+1]))
			}
			b.WriteString(f.Finish())
			if b.String() != tc.want {
				t.Fatalf("byte chunks got %q want %q", b.String(), tc.want)
			}
		})
	}
}

func TestRegistryPreservesToolChoiceAndHidesInlineReasoning(t *testing.T) {
	for _, mode := range []string{"generate", "stream"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Stream     bool `json:"stream"`
					ToolChoice struct {
						Type     string `json:"type"`
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.ToolChoice.Type != "function" || body.ToolChoice.Function.Name != "agent_finalize" {
					t.Errorf("tool_choice lost: %+v", body.ToolChoice)
				}
				if body.Stream {
					writeChatEvents(w, `{"choices":[{"index":0,"delta":{"content":"<think>private reasoning</think>visible answer","tool_calls":[{"index":0,"id":"call_1","function":{"name":"agent_finalize","arguments":"{\"content\":\"visible answer\"}"}}]},"finish_reason":"tool_calls"}]}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"model":"mimo-test","choices":[{"message":{"role":"assistant","content":"<think>private reasoning</think>visible answer","tool_calls":[{"id":"call_1","type":"function","function":{"name":"agent_finalize","arguments":"{\"content\":\"visible answer\"}"}}]},"finish_reason":"tool_calls"}]}`)
			}))
			defer server.Close()
			registry, selection, err := NewProviderRegistryFromProfiles(NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test-key", BaseURL: server.URL + "/v1", APIFormat: APIFormatChatCompletions, Model: "mimo-test"}))
			if err != nil {
				t.Fatal(err)
			}
			client := RegistryClient{Registry: registry, Selection: selection}
			req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "finish"}}, Tools: []ToolDefinition{{Name: "agent_finalize", Parameters: map[string]any{"type": "object"}}}, ToolChoice: "agent_finalize"}
			var text string
			var calls []ToolCall
			if mode == "generate" {
				out, err := client.Generate(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				text = out.Text
				calls = out.ToolCalls
			} else {
				events, err := client.Stream(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				for event := range events {
					if event.Type == ChatEventError {
						t.Fatal(event.Error)
					}
					text += event.Text
					if event.ToolCall != nil {
						calls = append(calls, *event.ToolCall)
					}
				}
			}
			if text != "visible answer" || len(calls) != 1 || calls[0].Name != "agent_finalize" {
				t.Fatalf("text=%q calls=%+v", text, calls)
			}
		})
	}
}

func TestOpenAIChatStreamHidesSplitInlineReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range []string{"<thi", "nk>private", " reasoning</thi", "nk>", "visible", " answer"} {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": piece}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test-key", BaseURL: server.URL + "/v1", Model: "mimo-test", APIFormat: APIFormatChatCompletions}, server.Client())
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for event := range events {
		if event.Type == ChatEventError {
			t.Fatal(event.Error)
		}
		if event.Type == ChatEventTextDelta {
			text.WriteString(event.Text)
		}
	}
	if text.String() != "visible answer" {
		t.Fatalf("text=%q", text.String())
	}
}
