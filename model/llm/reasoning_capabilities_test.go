package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReasoningCapabilityResolution(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: "https://private.example/v1", Model: "gpt-6-sol", ReasoningEffort: "none"}
	for _, tc := range []struct{ model, effort, want string }{
		{"gpt-6.1-sol", "none", "low"}, {"openai/gpt-6.1-sol", "minimal", "low"},
		{"gpt-6.1-sol", "ultra", "max"}, {"gpt-6.1-sol", "high", "high"},
		{"gpt-6-sol", "none", "none"}, {"unknown-model", "none", "none"},
		{"gpt-6.1-sol", "", ""}, {"gpt-6.1-sol", "bogus", "bogus"},
	} {
		if got := cfg.ResolveReasoningEffort(tc.model, tc.effort); got != tc.want {
			t.Errorf("%s %q = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
	req := (GenerateRequest{Model: "gpt-6.1-sol"}).withDefaults(cfg)
	if req.ReasoningEffort != "low" || cfg.ReasoningEffort != "none" {
		t.Fatalf("override/immutability: req=%q cfg=%q", req.ReasoningEffort, cfg.ReasoningEffort)
	}
}

func TestReasoningCatalogAmbiguousAndMissingMetadata(t *testing.T) {
	c := newModelsDevCatalogFromJSON([]byte(`{"a":{"models":{"shared":{"reasoning_options":[{"type":"effort","values":["low","high"]}]} }},"b":{"models":{"shared":{"reasoning_options":[{"type":"effort","values":["none","high"]}]},"toggle":{"reasoning_options":[{"type":"toggle"}]},"default-only":{"reasoning_options":[{"type":"effort","values":["default",null]}]}}}}`))
	for _, model := range []string{"shared", "vendor/shared", "toggle", "default-only", "missing"} {
		if got := c.ReasoningEfforts(ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: "https://private.example"}, model); len(got) != 0 {
			t.Fatalf("%s: %v", model, got)
		}
	}
}

func TestReasoningCapabilitiesOnWire(t *testing.T) {
	for _, format := range []APIFormat{APIFormatResponses, APIFormatChatCompletions} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", format, stream), func(t *testing.T) {
				bodies := make(chan map[string]any, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					bodies <- body
					if format == APIFormatChatCompletions {
						if stream {
							writeChatEvents(w, `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
						} else {
							fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
						}
						return
					}
					response := `{"id":"resp_test","object":"response","status":"completed","output":[{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, response)
					}
				}))
				defer server.Close()
				client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL, Model: "gpt-6-sol", APIFormat: format, ReasoningEffort: "high"}, server.Client())
				req := GenerateRequest{Model: "gpt-6.1-sol", ReasoningEffort: "none", Messages: []Message{{Role: RoleUser, Content: "ok"}}}
				if stream {
					events, err := client.Stream(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					done := false
					for e := range events {
						if e.Type == ChatEventError {
							t.Fatal(e.Error)
						}
						if e.Type == ChatEventDone {
							done = true
						}
					}
					if !done {
						t.Fatal("no completion")
					}
				} else if _, err := client.Generate(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				body := <-bodies
				got := body["reasoning_effort"]
				if format == APIFormatResponses {
					got = body["reasoning"].(map[string]any)["effort"]
				}
				if got != "low" {
					t.Fatalf("wire effort=%v", got)
				}
			})
		}
	}
}

func TestReasoningCatalogPrefersProviderThenCanonical(t *testing.T) {
	c := newModelsDevCatalogFromJSON([]byte(`{"maker":{"api":"https://maker.example","models":{"test":{"canonical_model_id":"maker/test","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},"relay":{"api":"https://relay.example","models":{"test":{"canonical_model_id":"maker/test","reasoning_options":[{"type":"effort","values":["none","high"]}]}}}}`))
	for _, tc := range []struct{ url, model, want string }{
		{"https://relay.example/v1", "test", "none"}, {"https://private.example/v1", "test", "low"}, {"https://private.example/v1", "maker/test", "low"},
	} {
		got := c.ReasoningEfforts(ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: tc.url}, tc.model)
		if len(got) == 0 || got[0] != tc.want {
			t.Fatalf("%s %s: %v", tc.url, tc.model, got)
		}
	}
}
