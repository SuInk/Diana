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
	"testing"
)

func TestContinuationStateIsIsolatedAcrossModelsAndEndpoints(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gemini", BaseURL: "https://gateway.example", APIFormat: APIFormat("chat_completions")}
	reasoning := "private reasoning"
	messages := []Message{{Role: RoleAssistant, Content: "working", ContinuationScope: continuationScope(cfg, ""), ReasoningContent: &reasoning,
		AnthropicThinking: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"private","signature":"sig"}`)},
		ResponsesOutput:   []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)},
		ToolCalls:         []ToolCall{{ID: "call", Name: "lookup", Arguments: map[string]any{"q": "test"}, ThoughtSignature: []byte("signature")}}},
		{Role: RoleTool, ToolCallID: "call", Content: "result"}}
	for _, change := range []string{"same", "model", "endpoint", "provider", "protocol"} {
		t.Run(change, func(t *testing.T) {
			target := cfg
			switch change {
			case "model":
				target.Model = "claude"
			case "endpoint":
				target.BaseURL = "https://other.example"
			case "provider":
				target.Provider = ProviderAnthropic
			case "protocol":
				target.APIFormat = APIFormat("responses")
			}
			got := (GenerateRequest{Messages: messages}).withDefaults(target).Messages
			m := got[0]
			if change == "same" {
				if m.ReasoningContent == nil || len(m.AnthropicThinking) != 1 || len(m.ResponsesOutput) != 1 || len(m.ToolCalls[0].ThoughtSignature) == 0 {
					t.Fatal("same-model continuation lost")
				}
			} else {
				if m.ReasoningContent != nil || len(m.AnthropicThinking) != 0 || len(m.ResponsesOutput) != 0 || len(m.ToolCalls[0].ThoughtSignature) != 0 {
					t.Fatal("foreign continuation survived")
				}
			}
			if m.Content != "working" || m.ToolCalls[0].ID != "call" || m.ToolCalls[0].Arguments["q"] != "test" || got[1].Content != "result" || got[1].ToolCallID != "call" {
				t.Fatal("conversation or tool pairing changed")
			}
			if len(messages[0].ToolCalls[0].ThoughtSignature) == 0 || messages[0].ReasoningContent == nil {
				t.Fatal("caller history mutated")
			}
		})
	}
}

func TestAnthropicRejectsUnsignedThinkingFromHistory(t *testing.T) {
	reasoning := "foreign reasoning"
	msg := Message{Role: RoleAssistant, Content: "answer", ReasoningContent: &reasoning, AnthropicThinking: []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"unsigned"}`),
		json.RawMessage(`{"type":"thinking","thinking":"blank","signature":" "}`),
		json.RawMessage(`{"type":"text","text":"not thinking"}`),
		json.RawMessage(`{"type":"redacted_thinking","data":""}`),
		json.RawMessage(`{"type":"thinking","thinking":"signed","signature":"opaque-signature"}`),
		json.RawMessage(`{"type":"redacted_thinking","data":"opaque-data"}`),
	}}
	raw, err := json.Marshal(anthropicMessages([]Message{msg}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"unsigned", "blank", "not thinking", "foreign reasoning"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("unsafe block survived: %s", raw)
		}
	}
	for _, wanted := range []string{"opaque-signature", "opaque-data", "answer"} {
		if !strings.Contains(string(raw), wanted) {
			t.Fatalf("native history lost: %s", raw)
		}
	}
}

func TestContinuationScopeSurvivesRegistryMessageConversions(t *testing.T) {
	original := []Message{{Role: RoleAssistant, ContinuationScope: "origin"}}
	if got := chatMessagesToLegacy(legacyMessagesToChat(original)); got[0].ContinuationScope != "origin" {
		t.Fatal("scope lost")
	}
	events := make(chan ChatEvent, 1)
	events <- ChatEvent{Type: ChatEventDone, Response: &GenerateResponse{Model: "alias"}}
	close(events)
	for event := range scopeContinuationEvents(context.Background(), events, "origin") {
		if event.Response.ContinuationScope != "origin" {
			t.Fatal("stream scope lost")
		}
	}
}

func TestChatGatewayModelSwitchDropsForeignReasoning(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if calls > 1 {
			_, present := body.Messages[1]["reasoning_content"]
			if present != (body.Model == "gemini") {
				t.Errorf("model %s: reasoning present=%v", body.Model, present)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"reply","model":"server-alias","choices":[{"message":{"role":"assistant","content":"answer","reasoning_content":"private reasoning"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", BaseURL: server.URL, Model: "gemini", APIFormat: APIFormatChatCompletions}
	registry, selection, err := NewProviderRegistryFromProfiles(NewProfileSet(cfg))
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: selection}
	req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "question"}}}
	first, err := client.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContinuationScope == "" || first.ReasoningContent == nil {
		t.Fatal("response lost continuation metadata")
	}
	req.Messages = append(req.Messages, Message{Role: RoleAssistant, Content: first.Text, ContinuationScope: first.ContinuationScope, ReasoningContent: first.ReasoningContent}, Message{Role: RoleUser, Content: "continue"})
	if _, err = client.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// The target uses the same OpenAI-compatible gateway, but serves Claude.
	cfg.Model = "claude"
	target, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("requests=%d", calls)
	}
}
