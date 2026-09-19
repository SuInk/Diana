package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestTelegramOwnerUsernameFromAuthenticatedSender(t *testing.T) {
	msg := &telegramMessage{MessageID: 1, Chat: &telegramChat{ID: -100, Type: "supergroup"}, From: &telegramUser{ID: 70001, Username: "owneruser", FirstName: "error"}, Text: "模型切到 gpt-5.6-terra"}
	event := telegramMessageToEvent(msg, "42", "examplebot")
	event.ProfileID = "tg"
	for _, owner := range []string{"owneruser", "@owneruser", " @Owneruser ", "70001"} {
		cfg := BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: owner}
		if !cfg.IsOwnerEvent(event) || cfg.OwnerIDForEvent(event) != "70001" {
			t.Fatalf("owner %q not matched", owner)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var restored MessageEvent
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if !cfg.IsOwnerEvent(restored) || restored.UserID != event.UserID || cfg.OwnerID != owner {
			t.Fatal("persisted event lost identity or mutated configuration")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*telegramMessage)
	}{
		{"display name", func(m *telegramMessage) { m.From.Username = "someone"; m.From.FirstName = "owneruser" }},
		{"missing username", func(m *telegramMessage) { m.From.Username = "" }},
		{"anonymous sender", func(m *telegramMessage) { m.SenderChat = &telegramChat{ID: -100, Type: "supergroup"} }},
		{"bot account", func(m *telegramMessage) { m.From.IsBot = true }},
		{"mentioned owner", func(m *telegramMessage) { m.From.Username = "someone"; m.Text = "@owneruser 模型切到 gpt-5.6-terra" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyMsg := *msg
			copyFrom := *msg.From
			copyMsg.From = &copyFrom
			tc.change(&copyMsg)
			got := telegramMessageToEvent(&copyMsg, "42", "examplebot")
			if (BotConfig{Platform: PlatformTelegram, OwnerID: "owneruser"}).IsOwnerEvent(got) {
				t.Fatal("untrusted identity became owner")
			}
		})
	}
	cfg := BotConfig{ID: "other", Platform: PlatformTelegram, OwnerID: "owneruser"}
	if cfg.IsOwnerEvent(event) {
		t.Fatal("identity crossed robot profiles")
	}
	cfg.ID = "tg"
	event.Platform = PlatformOneBotV11
	if cfg.IsOwnerEvent(event) {
		t.Fatal("non-Telegram sender used Telegram username")
	}
}

func TestTelegramUsernameOwnerCanChangeModel(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "main", Name: "main", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "test", Model: "old", Models: []llm.ModelInfo{{ID: "old"}, {ID: "new"}}}}}}}
	cfg := BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: "@owneruser", ModelRoles: map[string]ModelRole{"chat": {ProfileID: "main", Model: "old"}}}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
	r.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "old"}, {ID: "new"}}, nil
	})
	event := telegramMessageToEvent(&telegramMessage{MessageID: 1, Chat: &telegramChat{ID: -100, Type: "supergroup"}, From: &telegramUser{ID: 70001, Username: "owneruser"}, Text: "切换模型"}, "42", "examplebot")
	event.ProfileID = "tg"
	if !r.relationshipPolicy(context.Background(), event).Owner {
		t.Fatal("username owner did not receive owner policy")
	}
	registry, err := r.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, r.relationshipPolicy(context.Background(), event), newTestLLMConfigTool(r, event))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, found := registry.Get("llm_config"); !found {
		t.Fatal("model configuration tool hidden from username owner")
	}
	if prompt := r.systemPrompt(event, nil); !strings.Contains(prompt, "当前发言者是主人") {
		t.Fatal("model prompt still treats owner as ordinary member")
	}
	if _, err := newTestLLMConfigTool(r, event).Run(context.Background(), map[string]any{"model": "new"}); err != nil {
		t.Fatal(err)
	}
	if r.Config().ModelRoles["chat"].Model != "new" || r.Config().OwnerID != "@owneruser" {
		t.Fatal("model not changed or saved owner was rewritten")
	}
	event.SenderUsername = "someone"
	event.SenderName = "owneruser"
	if _, err := newTestLLMConfigTool(r, event).Run(context.Background(), map[string]any{"model": "old"}); err == nil {
		t.Fatal("display-name impostor changed model")
	}
}
