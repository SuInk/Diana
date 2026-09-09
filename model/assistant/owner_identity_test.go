package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestTelegramOwnerUsernameFromAuthenticatedSender(t *testing.T) {
	msg := &telegramMessage{MessageID: 1, Chat: &telegramChat{ID: -100, Type: "supergroup"}, From: &telegramUser{ID: 1061423117, Username: "ruaneko", FirstName: "error"}, Text: "模型切到 gpt-5.6-terra"}
	event := telegramMessageToEvent(msg, "42", "mikuabot")
	event.ProfileID = "tg"
	for _, owner := range []string{"ruaneko", "@ruaneko", " @Ruaneko ", "1061423117"} {
		cfg := BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: owner}
		if !cfg.IsOwnerEvent(event) || cfg.OwnerIDForEvent(event) != "1061423117" {
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
		{"display name", func(m *telegramMessage) { m.From.Username = "someone"; m.From.FirstName = "ruaneko" }},
		{"missing username", func(m *telegramMessage) { m.From.Username = "" }},
		{"anonymous sender", func(m *telegramMessage) { m.SenderChat = &telegramChat{ID: -100, Type: "supergroup"} }},
		{"bot account", func(m *telegramMessage) { m.From.IsBot = true }},
		{"mentioned owner", func(m *telegramMessage) { m.From.Username = "someone"; m.Text = "@ruaneko 模型切到 gpt-5.6-terra" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyMsg := *msg
			copyFrom := *msg.From
			copyMsg.From = &copyFrom
			tc.change(&copyMsg)
			got := telegramMessageToEvent(&copyMsg, "42", "mikuabot")
			if (BotConfig{Platform: PlatformTelegram, OwnerID: "ruaneko"}).IsOwnerEvent(got) {
				t.Fatal("untrusted identity became owner")
			}
		})
	}
	cfg := BotConfig{ID: "other", Platform: PlatformTelegram, OwnerID: "ruaneko"}
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
	cfg := BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: "@ruaneko", ModelRoles: map[string]ModelRole{"chat": {ProfileID: "main", Model: "old"}}}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
	r.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "old"}, {ID: "new"}}, nil
	})
	event := telegramMessageToEvent(&telegramMessage{MessageID: 1, Chat: &telegramChat{ID: -100, Type: "supergroup"}, From: &telegramUser{ID: 1061423117, Username: "ruaneko"}, Text: "切换模型"}, "42", "mikuabot")
	event.ProfileID = "tg"
	if !r.relationshipPolicy(context.Background(), event).Owner {
		t.Fatal("username owner did not receive owner policy")
	}
	registry, err := r.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, r.relationshipPolicy(context.Background(), event), newDianaLLMConfigTool(r, event))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, found := registry.Get("diana.llm_config"); !found {
		t.Fatal("model configuration tool hidden from username owner")
	}
	if prompt := r.systemPrompt(event, nil); !strings.Contains(prompt, "当前发言者是主人") {
		t.Fatal("model prompt still treats owner as ordinary member")
	}
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"model": "new"}); err != nil {
		t.Fatal(err)
	}
	if r.Config().ModelRoles["chat"].Model != "new" || r.Config().OwnerID != "@ruaneko" {
		t.Fatal("model not changed or saved owner was rewritten")
	}
	event.SenderUsername = "someone"
	event.SenderName = "ruaneko"
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"model": "old"}); err == nil {
		t.Fatal("display-name impostor changed model")
	}
}
