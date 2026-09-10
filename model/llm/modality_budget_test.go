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

func TestRegistryContextCapReachesGenerateAndStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct {
				Content []struct {
					Type   string `json:"type"`
					Detail string `json:"detail"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		images := 0
		for _, item := range body.Input {
			for _, part := range item.Content {
				if part.Type == "input_image" {
					images++
					if part.Detail != "low" {
						t.Error("request context cap was lost")
					}
				}
			}
		}
		if images != 2 {
			t.Errorf("images=%d", images)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":1,\"total_tokens\":101}}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "test", APIKey: "test", APIFormat: APIFormatResponses, BaseURL: server.URL, ContextWindowTokens: 128000}
	registry, selection, err := NewProviderRegistryFromProfiles(ProfileSet{Profiles: []Profile{{ID: "provider", Config: cfg}}})
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: selection}
	req := GenerateRequest{MaxContextTokens: 8000, MaxOutputTokens: 1024, Messages: []Message{{Role: RoleUser, Content: "look", Parts: []ContentPart{{Type: ContentPartText, Text: "look"}, {Type: ContentPartImageURL, ImageURL: "https://example.test/1.jpg", Detail: "high"}, {Type: ContentPartImageURL, ImageURL: "https://example.test/2.jpg", Detail: "high"}}}}}
	if _, err := client.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for event := range stream {
		if event.Type == ChatEventError {
			t.Fatal(event.Error)
		}
	}
}

func TestModalityBudgetBorrowingAndAttribution(t *testing.T) {
	for _, tc := range []struct {
		name                string
		textChars, images   int
		detail              string
		textOver, imageOver bool
	}{
		{"text borrows unused image quota", 45000, 1, "low", false, false},
		{"long text and few images", 60000, 1, "low", true, false},
		{"many images and short text", 300, 3, "high", false, true},
		{"both exceed their shares", 45000, 2, "high", true, true},
		{"only text", 61000, 0, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := Message{Role: RoleUser, Content: strings.Repeat("x", tc.textChars)}
			if tc.images > 0 {
				message.Parts = []ContentPart{{Type: ContentPartText, Text: message.Content}}
			}
			for i := 0; i < tc.images; i++ {
				message.Parts = append(message.Parts, ContentPart{Type: ContentPartImageURL, ImageURL: "image", Detail: tc.detail})
			}
			plan := PlanInputBudget(GenerateRequest{Messages: []Message{message}}, 20000)
			if (plan.TextExcess > 0) != tc.textOver || (plan.ImageExcess > 0) != tc.imageOver {
				t.Fatalf("wrong attribution: %+v", plan)
			}
			if plan.TextTokens+plan.ImageTokens+plan.OtherTokens != EstimateMessageTokens(message) {
				t.Fatal("double counted multipart content")
			}
		})
	}
}

func TestModalityBudgetCountsToolsAndReservesAudio(t *testing.T) {
	req := GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "test", Parts: []ContentPart{{Type: ContentPartInputAudio, AudioData: "AAAA"}}}}}
	before := PlanInputBudget(req, 20000)
	req.Tools = []ToolDefinition{{Name: "tool", Description: strings.Repeat("schema", 100), Parameters: map[string]any{"type": "object"}}}
	after := PlanInputBudget(req, 20000)
	if before.OtherTokens <= 0 || after.TextTokens <= before.TextTokens || after.OtherTokens != before.OtherTokens {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestTextOverageDoesNotLowerFewImages(t *testing.T) {
	message := Message{Role: RoleUser, Content: strings.Repeat("x", 60000), Parts: []ContentPart{{Type: ContentPartText, Text: strings.Repeat("x", 60000)}, {Type: ContentPartImageURL, ImageURL: "image", Detail: "high"}}}
	got := lowerProtectedImageDetailToFit([]Message{message}, 20000)
	if got[0].Parts[1].Detail != "high" {
		t.Fatal("text overage lowered image quality")
	}
}
