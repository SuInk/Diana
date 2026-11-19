package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestResponsesKeepsMultiImageInputBeforeTrailingSystem(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Input []struct {
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		images, low := 0, 0
		hasQuestion := false
		for _, item := range body.Input {
			var parts []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Detail string `json:"detail"`
			}
			if len(item.Content) == 0 || item.Content[0] != '[' {
				continue
			}
			if err := json.Unmarshal(item.Content, &parts); err != nil {
				t.Fatal(err)
			}
			for _, part := range parts {
				if part.Type == "input_image" {
					images++
					if part.Detail == "low" {
						low++
					}
				}
				hasQuestion = hasQuestion || strings.Contains(part.Text, "CURRENT_QUESTION")
			}
		}
		if images != 18 || low != 18 || !hasQuestion {
			t.Errorf("outgoing input lost current turn: images=%d low=%d question=%v", images, low, hasQuestion)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_test","object":"response","model":"gpt-test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`)
	}))
	defer server.Close()
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL, APIFormat: APIFormatResponses, Model: "gpt-test", MaxContextTokens: 128000, ContextWindowTokens: 128000, MaxOutputTokens: 1024}
	registry, selection, err := NewProviderRegistryFromProfiles(ProfileSet{Profiles: []Profile{{ID: "test", Config: cfg}}})
	if err != nil {
		t.Fatal(err)
	}
	input := Message{Role: RoleUser, Content: "CURRENT_QUESTION " + strings.Repeat("测试", 3000)}
	input.Parts = append(input.Parts, ContentPart{Type: ContentPartText, Text: input.Content})
	for i := 0; i < 18; i++ {
		input.Parts = append(input.Parts, ContentPart{Type: ContentPartImageURL, ImageURL: fmt.Sprintf("https://example.test/%d.jpg", i), Detail: "high"})
	}
	for _, explicit := range []bool{false, true} {
		if explicit {
			input.Priority = MessagePriorityCurrent
		}
		messages := []Message{{Role: RoleSystem, Content: strings.Repeat("规则", 600)}, input, {Role: RoleSystem, Content: "🍅：西红柿 / tomato"}}
		plan := PlanContextBudget(messages, 128000, 1024)
		for _, category := range plan.Categories {
			if category.Category == "current" && category.DroppedMessages != 0 {
				t.Fatalf("current input dropped: %+v", plan)
			}
		}
		response, err := (RegistryClient{Registry: registry, Selection: selection}).Generate(context.Background(), GenerateRequest{Messages: messages})
		if err != nil || response.Text != "ok" {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	}
	if calls != 2 {
		t.Fatalf("HTTP calls=%d", calls)
	}
}

func TestRegistryPreservesContextMetadata(t *testing.T) {
	source := []Message{{Role: RoleUser, Content: "fact", Priority: MessagePriorityPlugin, ContextGroup: "evidence", AtomicText: true, CacheBreakpoint: true}, {Role: RoleTool, Content: "result", ToolCallID: "call-1", Priority: MessagePriorityCurrent}}
	if got := chatMessagesToLegacy(legacyMessagesToChat(source)); !reflect.DeepEqual(got, source) {
		t.Fatalf("lost context metadata: %+v", got)
	}
}

func TestResponsesRejectsEmptyInputLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("empty input reached upstream")
		w.WriteHeader(400)
	}))
	defer server.Close()
	client := newOpenAICompatibleClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", BaseURL: server.URL, Model: "gpt-test", APIFormat: APIFormatResponses}, server.Client())
	req := GenerateRequest{Messages: []Message{{Role: RoleSystem, Content: "system only"}}}
	if _, err := client.Generate(context.Background(), req); !errors.Is(err, ErrMissingMessages) {
		t.Fatalf("Generate error=%v", err)
	}
	if _, err := client.Stream(context.Background(), req); !errors.Is(err, ErrMissingMessages) {
		t.Fatalf("Stream error=%v", err)
	}
}
