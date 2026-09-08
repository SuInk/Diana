package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRuntimeModelCatalogAllPurposes(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "chat-model", ImageModel: "image-model", APIKey: "secret-catalog-key", BaseURL: "https://private-catalog.invalid"})}
	r := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{"intent": {Group: llm.GroupChat, Model: "intent-model"}, PurposeMemoryExtract: {Group: llm.GroupChat, Model: "memory-model"}}}, nilChannel{}, NewDefaultPluginManager(), store, nil, nil, nil)
	body, err := newDianaRuntimeModelTool(newRuntimeAgentLLMProvider(r, context.Background())).Run(context.Background(), map[string]any{"group": "all"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Entries []modelCatalogEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != len(ModelBindingKeys())+2 {
		t.Fatalf("missing purposes: %s", body)
	}
	byPurpose := map[string]modelCatalogEntry{}
	for _, entry := range result.Entries {
		byPurpose[entry.Purpose] = entry
	}
	if byPurpose["intent"].Models[0].ModelID != "intent-model" || byPurpose[PurposeMemoryExtract].Models[0].ModelID != "memory-model" {
		t.Fatalf("purpose override lost: %s", body)
	}
	if byPurpose["tts"].Models[0].ModelID != "" || byPurpose["tts"].Note == "" {
		t.Fatal("external TTS weights were invented")
	}
	if byPurpose["stt"].Enabled == nil || *byPurpose["stt"].Enabled {
		t.Fatal("disabled STT shown as active")
	}
	for _, secret := range []string{"secret-catalog-key", "private-catalog.invalid"} {
		if strings.Contains(body, secret) {
			t.Fatal("catalog leaked credentials")
		}
	}
}
