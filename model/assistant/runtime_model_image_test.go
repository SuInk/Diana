package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRuntimeModelToolImageRouteWithoutChatCall(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		cfg := BotConfig{}
		want := "image-configured"
		if explicit {
			cfg.ModelRoles = map[string]ModelRole{"image": {Group: llm.GroupChat, Model: "image-role-override"}}
			want = "image-role-override"
		}
		store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible, Model: "chat-only", ImageModel: "image-configured",
			APIKey: "private-test-key", BaseURL: "https://private-endpoint.invalid/v1",
		})}
		runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
		tool := newDianaRuntimeModelTool(newRuntimeAgentLLMProvider(runtime, context.Background()))
		body, err := tool.Run(context.Background(), map[string]any{"group": "image"})
		if err != nil {
			t.Fatal(err)
		}
		var result dianaRuntimeModelResult
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
		if result.ModelID != want || result.Group != llm.GroupImage || result.Source != "configured_image_route" || len(result.ImageModels) != 1 {
			t.Fatalf("unexpected image identity: %+v", result)
		}
		for _, secret := range []string{"private-test-key", "private-endpoint.invalid", "chat-only"} {
			if strings.Contains(body, secret) {
				t.Fatalf("result leaked unrelated configuration: %s", body)
			}
		}
		if _, err := tool.Run(context.Background(), map[string]any{"group": "unsupported"}); err == nil {
			t.Fatal("invalid group silently returned chat identity")
		}
	}
}

func TestRuntimeModelToolImageRouteMissingConfig(t *testing.T) {
	tool := newDianaRuntimeModelTool(&runtimeAgentLLMProvider{})
	if _, err := tool.Run(context.Background(), map[string]any{"group": "image"}); err == nil {
		t.Fatal("missing configuration must not invent an image model")
	}
}

func TestRuntimeModelToolImageFallbackOrder(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "first", Group: llm.GroupChat, Config: llm.ProviderConfig{Model: "chat-first", ImageModel: "image-first"}},
		{ID: "second", Group: llm.GroupChat, Config: llm.ProviderConfig{Model: "chat-second", ImageModel: "image-second"}},
	}}}
	runtime := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{"chat": {Group: llm.GroupChat, Model: "chat-first"}}}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	body, err := newDianaRuntimeModelTool(newRuntimeAgentLLMProvider(runtime, context.Background())).Run(context.Background(), map[string]any{"group": "image"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRuntimeModelResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.ImageModels) != 2 || result.ImageModels[0].ModelID != "image-first" || result.ImageModels[1].ModelID != "image-second" {
		t.Fatalf("wrong fallback order: %+v", result.ImageModels)
	}
}
