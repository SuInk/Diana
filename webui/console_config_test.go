package webui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
)

func TestConsoleConfigProviderPatchPreservesUnsubmittedSettings(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "provider-secret-value", BaseURL: "https://private.example/v1", Model: "gpt-test", Timeout: 12345 * time.Millisecond})
	m := NewConsoleConfigManager(nil, NewLLMConfigHandler(store), nil, nil, nil, nil)
	id := store.Profiles().Profiles[0].ID
	result, err := m.Apply(context.Background(), "bot", "provider", "update", id, map[string]any{"name": "新名称"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Current()
	if cfg.APIKey != "provider-secret-value" || cfg.BaseURL != "https://private.example/v1" || cfg.Timeout != 12345*time.Millisecond || cfg.Model != "gpt-test" {
		t.Fatal("patch changed unsubmitted fields")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private.example") || strings.Contains(string(raw), "provider-secret-value") {
		t.Fatalf("leaked response: %s", raw)
	}
	if _, err = m.Apply(context.Background(), "bot", "provider", "create", "", map[string]any{"id": id}); err == nil {
		t.Fatal("create accepted existing identity")
	}
	if _, err = m.Apply(context.Background(), "bot", "provider", "update", id, map[string]any{"base_url": "[REDACTED]"}); err == nil {
		t.Fatal("saved redaction marker")
	}
}

func TestConsoleConfigBotAdvancedFieldsAndModelRoutes(t *testing.T) {
	cfg := assistant.DefaultBotConfig()
	cfg.ID = "bot-a"
	cfg.OwnerID = "owner"
	cfg.Enabled = false
	cfg.ModelRoles = map[string]assistant.ModelRole{"chat": {ProfileID: "p", Model: "first", ReasoningEffort: "high", Fallbacks: []assistant.ModelRole{{ProfileID: "p", Model: "second", ReasoningEffort: "low"}}}}
	r := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), struct{ BotRuntime }{r})
	m := NewConsoleConfigManager(h, nil, nil, nil, nil, nil)
	_, err := m.Apply(context.Background(), "bot-a", "bot", "update", "", map[string]any{"owner_login_enabled": false, "model_roles": map[string]any{"chat": map[string]any{"profile_id": "p", "model": "second", "reasoning_effort": "low", "routing_strategy": "weighted", "weight": 2}}})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := h.profiles.Profiles().ConfigForProfile("bot-a")
	if saved.OwnerLoginEnabled || saved.ModelRoles["chat"].Model != "second" || saved.ModelRoles["chat"].ReasoningEffort != "low" || len(saved.ModelRoles["chat"].Fallbacks) != 0 {
		t.Fatal("advanced settings not applied")
	}
	if _, err = m.Apply(context.Background(), "bot-a", "bot", "update", "other", map[string]any{"owner_id": "other"}); err == nil {
		t.Fatal("changed other bot")
	}
}

func TestAdminChatMessagesRedactedWithoutMutatingHistory(t *testing.T) {
	original := []adminChatMessage{{Role: "user", Content: "密钥: secret-value URL https://private.example"}}
	out := redactedAdminChatMessages(original, func(s string) string { return strings.ReplaceAll(s, "secret-value", "[REDACTED]") })
	if strings.Contains(out[0].Content, "secret-value") || !strings.Contains(original[0].Content, "secret-value") {
		t.Fatal("redaction mutated source or failed")
	}
}

func TestConsoleConfigTrustedAppUsesSubmittedSecretAndRedactsIt(t *testing.T) {
	_, trusted, _ := managedFixture(t)
	manager := NewConsoleConfigManager(nil, nil, nil, nil, nil, trusted)
	secret := "supplied-app-secret-1234567890-abcdef"
	result, err := manager.Apply(context.Background(), "bot-a", "trusted_apps", "create", "", map[string]any{"id": "chat-test", "name": "测试", "profile_id": "bot-a", "disabled": false, "secret": secret})
	if err != nil {
		t.Fatal(err)
	}
	if trusted.apps["chat-test"].SecretSHA256 != hashToken(secret) {
		t.Fatal("didn't use submitted secret")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret echoed")
	}
	next := "replacement-app-secret-1234567890-abcdef"
	if _, err = manager.Apply(context.Background(), "bot-a", "trusted_apps", "rotate", "chat-test", map[string]any{"secret": next}); err != nil {
		t.Fatal(err)
	}
	if trusted.apps["chat-test"].SecretSHA256 != hashToken(next) {
		t.Fatal("rotation failed")
	}
}
