package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 调用方收紧的上下文上限一路带到 Generate 和 Stream：超出时两条路都在本地报
// ErrContextOverflow，不发请求、不降图片细节。
func TestRegistryContextCapReachesGenerateAndStream(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "test", APIKey: "test", APIFormat: APIFormatResponses, BaseURL: server.URL, ContextWindowTokens: 128000}
	registry, selection, err := NewProviderRegistryFromProfiles(ProfileSet{Profiles: []Profile{{ID: "provider", Config: cfg}}})
	if err != nil {
		t.Fatal(err)
	}
	client := RegistryClient{Registry: registry, Selection: selection}
	req := GenerateRequest{MaxContextTokens: 8000, MaxOutputTokens: 1024, Messages: []Message{{Role: RoleUser, Content: "look", Parts: []ContentPart{{Type: ContentPartText, Text: "look"}, {Type: ContentPartImageURL, ImageURL: "https://example.test/1.jpg", Detail: "high"}, {Type: ContentPartImageURL, ImageURL: "https://example.test/2.jpg", Detail: "high"}}}}}
	if _, err := client.Generate(context.Background(), req); !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("generate err = %v, want ErrContextOverflow", err)
	}
	stream, err := client.Stream(context.Background(), req)
	message := ""
	if err != nil {
		message = err.Error()
	} else {
		for event := range stream {
			if event.Type == ChatEventError {
				message = event.Error
			}
		}
	}
	if !IsContextOverflowError(errors.New(message)) {
		t.Fatalf("stream error = %q, want the context overflow", message)
	}
	if calls != 0 {
		t.Fatalf("oversized request reached HTTP %d times", calls)
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
