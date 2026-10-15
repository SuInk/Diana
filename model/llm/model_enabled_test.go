package llm

import (
	"encoding/json"
	"testing"
)

func TestDisabledCatalogModelSurvivesConfigAndCannotRun(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test-key", Model: "off", Models: []ModelInfo{{ID: "off", Disabled: true}, {ID: "on"}}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored ProviderConfig
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	restored = restored.WithDefaults()
	if restored.ModelEnabled("off") || !restored.ModelEnabled("on") || !restored.ModelEnabled("namespace/off") {
		t.Fatal("enablement must persist and use exact model IDs")
	}
	if _, err := NewClient(restored); err == nil {
		t.Fatal("disabled model must not create a client")
	}
	registry, active, err := NewProviderRegistryFromProfiles(NewProfileSet(restored))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range registry.Models() {
		if model.ModelID == "off" {
			t.Fatal("disabled model registered")
		}
	}
	if active.ModelID != "" {
		t.Fatal("disabled model selected as default")
	}
	restored.Model = "on"
	if _, err := NewClient(restored); err != nil {
		t.Fatal(err)
	}
	restored.Model = "off"
	if _, err := newMediaHTTP(restored, "https://example.com", nil); err == nil {
		t.Fatal("disabled media model accepted")
	}
}
