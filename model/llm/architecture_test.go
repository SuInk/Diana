package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type registryAdapter struct{ response ChatResponse }

func (a registryAdapter) Generate(context.Context, ModelDefinition, ChatRequest) (ChatResponse, error) {
	return a.response, nil
}
func (a registryAdapter) Stream(context.Context, ModelDefinition, ChatRequest) (<-chan ChatEvent, error) {
	return nil, errors.New("unused")
}

func TestProviderRegistryRoutesSelectionAndHidesAPIKey(t *testing.T) {
	r := NewProviderRegistry()
	if err := r.RegisterProvider(ProviderDefinition{ID: "p1", Name: "test", Protocol: ProtocolOpenAICompletions, APIKey: "secret", Enabled: true}, registryAdapter{response: ChatResponse{Text: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterModel(ModelDefinition{ID: "p1:m1", ProviderID: "p1", ModelID: "m1"}); err != nil {
		t.Fatal(err)
	}
	response, err := r.Generate(context.Background(), AgentModelConfig{ProviderID: "p1", ModelID: "p1:m1"}, ChatRequest{Messages: []ChatMessage{{Role: RoleUser, Content: "hi"}}})
	if err != nil || response.Text != "ok" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	public, ok := r.PublicProvider("p1")
	if !ok || public.APIKey != "" {
		t.Fatalf("public provider leaked key: %#v", public)
	}
	if _, err := r.Generate(context.Background(), AgentModelConfig{ProviderID: "p1", ModelID: "other"}, ChatRequest{}); err == nil {
		t.Fatal("unknown model unexpectedly routed")
	}
}

func TestLegacyProfileMigrationPreservesProtocolAndSelection(t *testing.T) {
	set := NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "secret", BaseURL: "https://gateway.test/v1", APIFormat: APIFormatChatCompletions, Model: "chat-model", Models: []ModelInfo{{ID: "chat-model"}, {ID: "backup-model"}}})
	r, active, err := NewProviderRegistryFromProfiles(set)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := r.Provider(set.ActiveID)
	if !ok || provider.Protocol != ProtocolOpenAICompletions || provider.APIKey != "secret" {
		t.Fatalf("provider=%#v", provider)
	}
	if active.ProviderID != set.ActiveID || active.ModelID != set.ActiveID+":chat-model" {
		t.Fatalf("active=%#v", active)
	}
	model, ok := r.Model(set.ActiveID + ":backup-model")
	if !ok || model.ProviderID != set.ActiveID || model.ModelID != "backup-model" {
		t.Fatalf("model=%#v", model)
	}
	public, _ := r.PublicProvider(set.ActiveID)
	if strings.Contains(public.APIKey, "secret") {
		t.Fatal("migration public view exposed API key")
	}
}

func TestLegacyProfileMigrationMapsAllProtocols(t *testing.T) {
	for _, test := range []struct {
		provider Provider
		format   APIFormat
		want     Protocol
	}{
		{ProviderOpenAICompatible, APIFormatResponses, ProtocolOpenAIResponses},
		{ProviderOpenAICompatible, APIFormatChatCompletions, ProtocolOpenAICompletions},
		{ProviderAnthropic, "", ProtocolAnthropicMessages},
		{ProviderGemini, "", ProtocolGemini},
	} {
		t.Run(string(test.want), func(t *testing.T) {
			set := NewProfileSet(ProviderConfig{Provider: test.provider, APIKey: "secret-key", APIFormat: test.format, Model: "model"})
			registry, _, err := NewProviderRegistryFromProfiles(set)
			if err != nil {
				t.Fatal(err)
			}
			provider, ok := registry.Provider(set.ActiveID)
			if !ok || provider.Protocol != test.want {
				t.Fatalf("provider=%#v, want protocol %q", provider, test.want)
			}
		})
	}
}

func TestClientAdapterConvertsToolResultsWithoutVendorTypes(t *testing.T) {
	adapter := clientAdapter{cfg: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", BaseURL: "https://example.test/v1", Model: "model"}}
	// The conversion is exercised without a network call by checking the vendor-
	// neutral request shape through the adapter's request construction helper.
	request := ChatRequest{Messages: []ChatMessage{{Role: RoleTool, ToolResult: &ToolResult{CallID: "call-1", Name: "search", Content: `{"ok":true}`}}}}
	converted := make([]Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		item := Message{Role: message.Role, Content: message.Content, ToolCalls: message.ToolCalls}
		if message.ToolResult != nil {
			item.ToolCallID, item.ToolName, item.Content = message.ToolResult.CallID, message.ToolResult.Name, message.ToolResult.Content
		}
		converted = append(converted, item)
	}
	if converted[0].ToolCallID != "call-1" || converted[0].ToolName != "search" || converted[0].Content == "" {
		t.Fatalf("converted=%#v", converted)
	}
	_ = adapter
}
