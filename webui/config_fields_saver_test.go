package webui

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSaveBotFieldsUsesWebUIValidation(t *testing.T) {
	cfg := assistant.DefaultBotConfig()
	cfg.ID, cfg.OwnerID = "bot-a", "owner"
	r := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), struct{ BotRuntime }{r})
	ctx := context.Background()

	if _, err := h.SaveBotFields(ctx, "bot-a", func(c assistant.BotConfig) (assistant.BotConfig, error) {
		c.OneBotAccessToken = "short"
		return c, nil
	}); err == nil {
		t.Fatal("weak token accepted; chat path must match WebUI token check")
	}
	if _, err := h.SaveBotFields(ctx, "missing", func(c assistant.BotConfig) (assistant.BotConfig, error) { return c, nil }); err == nil {
		t.Fatal("unknown profile accepted")
	}
	saved, err := h.SaveBotFields(ctx, "bot-a", func(c assistant.BotConfig) (assistant.BotConfig, error) {
		c.SystemPrompt = "新人设"
		return c, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := h.profiles.Profiles().ConfigForProfile("bot-a")
	if saved.SystemPrompt != "新人设" || stored.SystemPrompt != "新人设" {
		t.Fatalf("not persisted: saved=%q stored=%q", saved.SystemPrompt, stored.SystemPrompt)
	}
	if r.ProfileConfig("bot-a").SystemPrompt != "新人设" {
		t.Fatal("runtime not hot-applied")
	}
}
