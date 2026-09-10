package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type scopedRoleTestSaver struct {
	configs map[string]BotConfig
	err     error
	calls   int
}

func (s *scopedRoleTestSaver) SaveBotConfig(BotConfig) { panic("unscoped save used") }
func (s *scopedRoleTestSaver) SaveModelRole(cfg BotConfig, key string, next ModelRole) (BotConfig, error) {
	if s.err != nil {
		return BotConfig{}, s.err
	}
	current, ok := s.configs[cfg.ID]
	if !ok {
		return BotConfig{}, errors.New("missing bot")
	}
	roles := normalizeModelRoles(current.ModelRoles)
	roles[key] = next
	current.ModelRoles = roles
	s.configs[cfg.ID] = current
	s.calls++
	return current, nil
}

func modelSwitchTestRuntime(t *testing.T) (*Runtime, *scopedRoleTestSaver, *stubLLMProfileStore, MessageEvent) {
	t.Helper()
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "one", Name: "Gateway A", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "secret-one", BaseURL: "https://one.invalid", Model: "shared", Models: []llm.ModelInfo{{ID: "shared"}, {ID: "other"}}}},
		{ID: "two", Name: "Gateway B", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "secret-two", BaseURL: "https://two.invalid", Model: "shared", Models: []llm.ModelInfo{{ID: "shared"}, {ID: "other"}}}},
	}}}
	a := BotConfig{ID: "a", OwnerID: "11", ModelRoles: map[string]ModelRole{"chat": {ProfileID: "one", Model: "shared"}, "intent": {ProfileID: "one", Model: "shared"}}}.WithDefaults()
	b := BotConfig{ID: "b", OwnerID: "22", ModelRoles: map[string]ModelRole{"chat": {ProfileID: "one", Model: "shared"}, "vision": {ProfileID: "one", Model: "shared"}, "intent": {ProfileID: "one", Model: "shared"}, "image": {ProfileID: "one", Model: "shared"}}}.WithDefaults()
	saver := &scopedRoleTestSaver{configs: map[string]BotConfig{"a": a, "b": b}}
	r := NewRuntime(a, nilChannel{}, NewPluginManager(), store, nil, saver, nil)
	r.SetProfiles(ProfileSet{ActiveID: "a", Profiles: []BotConfig{a, b}})
	r.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "shared"}, {ID: "other"}}, nil
	})
	r.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		return &modelSwitchTestClient{LLMProvider: &capturingLLMProvider{reply: cfg.BaseURL + "|" + cfg.Model}}, nil
	})
	return r, saver, store, MessageEvent{Kind: EventKindPrivate, ProfileID: "b", UserID: "22", MessageID: "m"}
}

func TestModelSwitchTargetsSenderBotAndSpecificProvider(t *testing.T) {
	r, saver, store, event := modelSwitchTestRuntime(t)
	tool := newDianaLLMConfigTool(r, event)
	list, err := tool.Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil || !strings.Contains(list, "Gateway B") || strings.Contains(list, "secret-") || strings.Contains(list, "https://") {
		t.Fatalf("unsafe or incomplete listing: %s %v", list, err)
	}
	for _, role := range []string{"chat", "vision", "intent", "image"} {
		if _, err := tool.Run(context.Background(), map[string]any{"role": role, "provider_id": "two", "model": "other"}); err != nil {
			t.Fatal(err)
		}
		if r.Config().ID != "a" || r.Config().ModelRoles["chat"].ProfileID != "one" {
			t.Fatal("changed active robot")
		}
		if saver.configs["b"].ModelRoles[role].ProfileID != "two" || r.effectiveConfigForEvent(event).ModelRoles[role].Model != "other" {
			t.Fatal("target robot binding not saved or applied")
		}
	}
	ctx := withLLMUsageContext(context.Background(), event)
	run := func(p LLMProvider) (string, error) {
		result, err := p.Generate(ctx, llm.GenerateRequest{})
		if err != nil {
			return "", err
		}
		return result.Text, nil
	}
	if got, err := r.runRawLLMProviderForGroup(ctx, llm.GroupChat, run); err != nil || got != "https://two.invalid|other" {
		t.Fatalf("next chat request used wrong provider: %s %v", got, err)
	}
	if got, err := r.runLLMRouterProviderWithRetry(ctx, false, run); err != nil || got != "https://two.invalid|other" {
		t.Fatalf("intent used wrong provider: %s %v", got, err)
	}
	if configs := r.imageProviderConfigs(ctx); len(configs) != 1 || configs[0].BaseURL != "https://two.invalid" || configs[0].ImageModelWithDefault() != "other" {
		t.Fatalf("image assignment not effective: %+v", configs)
	}
	if store.set.Profiles[0].Config.Model != "shared" || store.set.Profiles[1].Config.APIKey != "secret-two" {
		t.Fatal("provider configuration changed")
	}
}

func TestModelSwitchFailureLeavesRuntimeAndStoreUnchanged(t *testing.T) {
	r, saver, _, event := modelSwitchTestRuntime(t)
	saver.err = errors.New("disk full")
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatal(err)
	}
	if r.effectiveConfigForEvent(event).ModelRoles["chat"].ProfileID != "one" || saver.configs["b"].ModelRoles["chat"].ProfileID != "one" {
		t.Fatal("failed persistence changed configuration")
	}
	saver.err = nil
	for _, input := range []map[string]any{{"provider_id": "missing", "model": "shared"}, {"provider_id": "two", "model": "not-listed"}, {"provider_id": "two", "provider": "gemini", "model": "shared"}} {
		if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), input); err == nil {
			t.Fatalf("invalid selection accepted: %v", input)
		}
	}
	for _, e := range []MessageEvent{{ProfileID: "b", UserID: "11"}, {ProfileID: "missing", UserID: "11"}, {UserID: "11"}} {
		if _, err := newDianaLLMConfigTool(r, e).Run(context.Background(), map[string]any{"model": "other"}); err == nil {
			t.Fatal("unauthorized or ambiguous bot accepted")
		}
	}
	if saver.calls != 0 {
		t.Fatal("rejected requests reached storage")
	}
}

func TestExplicitProviderNameAndAmbiguity(t *testing.T) {
	r, _, store, event := modelSwitchTestRuntime(t)
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_name": "Gateway B", "model": "shared"}); err != nil {
		t.Fatal(err)
	}
	store.set.Profiles[0].Name = "Gateway B"
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_name": "Gateway B", "model": "shared"}); err == nil {
		t.Fatal("duplicate provider names silently selected")
	}
}

func TestScopedModelSwitchUsesRegistryOnNextRequest(t *testing.T) {
	r, _, _, event := modelSwitchTestRuntime(t)
	registry := llm.NewProviderRegistry()
	for _, id := range []string{"one", "two"} {
		if err := registry.RegisterProvider(llm.ProviderDefinition{ID: id, Name: id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, &retryRegistryAdapter{succeedAt: 1, response: id}); err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{"shared", "other"} {
			if err := registry.RegisterModel(llm.ModelDefinition{ID: id + ":" + model, ProviderID: id, ModelID: model}); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.SetLLMProviderRegistry(registry)
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other", "role": "intent"}); err != nil {
		t.Fatal(err)
	}
	ctx := withModelConfigEvent(context.Background(), event)
	run := func(p LLMProvider) (string, error) {
		result, err := p.Generate(ctx, llm.GenerateRequest{})
		if err != nil {
			return "", err
		}
		return result.Text, nil
	}
	if text, err := r.runLLMRouterProviderWithRetry(ctx, false, run); err != nil || text != "two" {
		t.Fatalf("registry intent selection wrong: %s %v", text, err)
	}
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "shared"}); err != nil {
		t.Fatal(err)
	}
	if text, err := r.runRawLLMProviderForGroup(ctx, llm.GroupChat, run); err != nil || text != "two" {
		t.Fatalf("registry chat selection wrong: %s %v", text, err)
	}
	aCtx := withModelConfigEvent(context.Background(), MessageEvent{ProfileID: "a"})
	if text, err := r.runRawLLMProviderForGroup(aCtx, llm.GroupChat, run); err != nil || text != "one" {
		t.Fatalf("registry leaked to other bot: %s %v", text, err)
	}
}

func TestConcurrentModelSwitchPreservesBothRoles(t *testing.T) {
	r, saver, _, event := modelSwitchTestRuntime(t)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, role := range []string{"chat", "vision"} {
		wg.Add(1)
		go func(role string) {
			defer wg.Done()
			_, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other", "role": role})
			errors <- err
		}(role)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"chat", "vision"} {
		if saver.configs["b"].ModelRoles[role].ProfileID != "two" || r.effectiveConfigForEvent(event).ModelRoles[role].ProfileID != "two" {
			t.Fatal("concurrent update lost a role")
		}
	}
}
