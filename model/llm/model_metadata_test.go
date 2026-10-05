// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package llm

import "testing"

func TestValidateModelOverridesAndNormalizeWithoutMutatingInput(t *testing.T) {
	capabilities := &ModelCapabilities{InputModalities: []string{" TEXT ", "image", "image"}, OutputModalities: []string{"TEXT"}}
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", Model: "m", Models: []ModelInfo{{ID: "m", Custom: true, ContextWindowOverride: 8192, CapabilitiesOverride: capabilities}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	got := cfg.WithDefaults().Models[0]
	if len(got.CapabilitiesOverride.InputModalities) != 2 || got.CapabilitiesOverride.InputModalities[0] != "text" || got.CapabilitiesOverride.OutputModalities[0] != "text" {
		t.Fatalf("capabilities not normalized: %#v", got)
	}
	if capabilities.InputModalities[0] != " TEXT " {
		t.Fatal("normalization mutated caller metadata")
	}
	cfg.Models[0].ContextWindowOverride = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative window accepted")
	}
	cfg.Models[0].ContextWindowOverride = 0
	cfg.Models[0].CapabilitiesOverride = &ModelCapabilities{InputModalities: []string{"text"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty output capabilities accepted")
	}
}

func TestRegistryUsesEachModelsOwnContextOverride(t *testing.T) {
	set := NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key", Model: "text",
		Models: []ModelInfo{{ID: "text", ContextWindowOverride: 8192}, {ID: "vision", ContextWindowOverride: 64000}},
	})
	registry, _, err := NewProviderRegistryFromProfiles(set)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int64{"text": 8192, "vision": 64000} {
		model, ok := registry.Model(set.Profiles[0].ID + ":" + id)
		if !ok || model.ContextWindow != want {
			t.Fatalf("model %s window = %d, want %d", id, model.ContextWindow, want)
		}
	}
}
