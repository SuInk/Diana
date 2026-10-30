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
	"testing"
)

func TestOpenAIDecisionsContract(t *testing.T) {
	spec := typeSafeTestSpec()
	spec.Questions = append(spec.Questions, DecisionQuestion{Key: "route", Kind: DecisionChoice, Instructions: "选择路由", Options: []DecisionOption{{Value: "reply"}, {Value: "skip"}}, Path: "route"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/decisions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		var payload struct {
			Input     []map[string]any         `json:"input"`
			Questions []openAIDecisionQuestion `json:"questions"`
			CacheKey  string                   `json:"prompt_cache_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(payload.Input)
		if !strings.Contains(string(encoded), "本轮回应提问") || !strings.Contains(string(encoded), "https://example.com/image.png") {
			t.Errorf("lost evidence: %s", encoded)
		}
		if len(payload.Questions) != 3 || payload.Questions[0].Type != "predicate" || !strings.Contains(payload.Questions[0].Instructions, "群友彼此聊天") || payload.CacheKey != "test-cache" {
			t.Errorf("bad question mapping: %+v", payload)
		}
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"name":"route","type":"choice","choice":"reply","confidence":0.8},{"name":"chat_in","type":"score","score":1,"confidence":0.7},{"name":"relevance","type":"predicate","probability":0.91}],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024}}}`)
	}))
	defer server.Close()
	for _, suffix := range []string{"", "/v1", "/v1/decisions"} {
		client, err := NewClient(ProviderConfig{Provider: ProviderOpenAIDecisions, APIKey: "key", BaseURL: server.URL + suffix}, WithHTTPClient(server.Client()))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Generate(context.Background(), GenerateRequest{PromptCacheKey: "test-cache", Decision: spec, Messages: []Message{{Role: RoleSystem, Content: "本轮回应提问"}, {Role: RoleUser, Parts: []ContentPart{{Type: ContentPartText, Text: "diana 看看"}, {Type: ContentPartImageURL, ImageURL: "https://example.com/image.png"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Provider != ProviderOpenAIDecisions || resp.Usage.CachedInputTokens != 1024 || !strings.Contains(resp.Text, `"score":0.5`) || !strings.Contains(resp.Text, `"directed":true`) || !strings.Contains(resp.Text, `"route":"reply"`) {
			t.Fatalf("bad result: %+v", resp)
		}
	}
}

func TestOpenAIDecisionsRejectsInvalidAnswers(t *testing.T) {
	for _, answer := range []string{
		`[]`,
		`[{"name":"relevance","type":"refusal"}]`,
		`[{"name":"relevance","type":"predicate"}]`,
		`[{"name":"relevance","type":"predicate","probability":2}]`,
		`[{"name":"relevance","type":"choice","choice":"yes"}]`,
		`[{"name":"other","type":"predicate","probability":1}]`,
		`[{"name":"relevance","type":"predicate","probability":1},{"name":"relevance","type":"predicate","probability":1}]`,
	} {
		t.Run(answer, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"answers":`+answer+`}`) }))
			defer server.Close()
			client := newOpenAIDecisionsClient(ProviderConfig{APIKey: "key", BaseURL: server.URL}, server.Client())
			_, err := client.Generate(context.Background(), GenerateRequest{Decision: typeSafeTestSpec(), Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if err == nil {
				t.Fatal("accepted invalid answer")
			}
		})
	}
}
func TestOpenAIDecisionsErrors(t *testing.T) {
	client := newOpenAIDecisionsClient(ProviderConfig{}, nil)
	if _, err := client.Generate(context.Background(), GenerateRequest{}); !errors.Is(err, ErrDecisionRequired) {
		t.Fatal(err)
	}
	if _, err := client.Generate(context.Background(), GenerateRequest{Decision: typeSafeTestSpec(), Tools: []ToolDefinition{{Name: "search"}}}); !errors.Is(err, ErrDecisionRequired) {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer server.Close()
	client = newOpenAIDecisionsClient(ProviderConfig{APIKey: "key", BaseURL: server.URL}, server.Client())
	_, err := client.Generate(context.Background(), GenerateRequest{Decision: typeSafeTestSpec(), Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatal(err)
	}
}
func TestOpenAIDecisionsRegistryRoundTrip(t *testing.T) {
	registry, err := RegistryFromDocument(ProviderRegistryDocument{Version: 1, Providers: []ProviderDefinition{{ID: "decisions", Protocol: ProtocolOpenAIDecisions, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Document().Providers[0].Protocol != ProtocolOpenAIDecisions || protocolForConfig(ProviderConfig{Provider: ProviderOpenAIDecisions}) != ProtocolOpenAIDecisions {
		t.Fatal("protocol lost")
	}
}

func TestOpenAIDecisionsListsOnlySupportedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("bad model path: %s", r.URL.Path)
		}
		io.WriteString(w, `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-sol"},{"id":"text-embedding-3-small"}]}`)
	}))
	defer server.Close()
	models, err := ListModels(context.Background(), ProviderConfig{Provider: ProviderOpenAIDecisions, APIKey: "key", BaseURL: server.URL + "/v1/decisions"}, WithHTTPClient(server.Client()))
	if err != nil || len(models) != 1 || models[0].ID != "gpt-6-luna" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestOpenAIDecisionsInputRejectsUnsupportedEvidence(t *testing.T) {
	for _, messages := range [][]Message{
		nil,
		{{Role: RoleTool, Content: "tool result"}},
		{{Role: RoleUser, Parts: []ContentPart{{Type: ContentPartInputAudio, AudioData: "data"}}}},
		{{Role: RoleSystem, Parts: []ContentPart{{Type: ContentPartImageURL, ImageURL: "https://example.com/image.png"}}}},
	} {
		if _, err := openAIDecisionInput(messages); err == nil {
			t.Fatalf("accepted %+v", messages)
		}
	}
}
