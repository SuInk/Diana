// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Opt-in replay of LLM welcomes with the production model, persona and the real
// group history right before each join. Nothing is sent; the generated text is
// only logged.
func TestLiveWelcomeGenerationReplay(t *testing.T) {
	path := os.Getenv("DIANA_WELCOME_GENERATION_BUNDLE")
	if path == "" {
		t.Skip("set DIANA_WELCOME_GENERATION_BUNDLE to a private production replay bundle")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		BotConfig BotConfig
		Profiles  llm.ProfileSet
		Registry  llm.ProviderRegistryDocument
		Cases     []struct {
			Event   MessageEvent
			History []MessageEvent
		}
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal("invalid private replay bundle")
	}
	if bundle.BotConfig.ID == "" || len(bundle.Cases) == 0 {
		t.Fatal("requires production config and join cases")
	}
	registry, err := llm.RegistryFromDocument(bundle.Registry)
	if err != nil {
		t.Fatal("cannot initialize production model registry")
	}
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "replay.db"))
	cfg := bundle.BotConfig.WithDefaults()
	// 每条间隔都超过 1 秒，冷却不会把回放里的入群挡成回落。
	cfg.WelcomeEnabled, cfg.WelcomeMode, cfg.WelcomeLLMCooldownSeconds = true, WelcomeModeLLM, 1
	channel := &recordingChannel{}
	r := NewRuntime(cfg, channel, NewPluginManager(), &stubLLMProfileStore{set: bundle.Profiles}, nil, nil, nil)
	r.SetLLMProviderRegistry(registry)
	var results []map[string]string
	for _, tc := range bundle.Cases {
		for _, event := range tc.History {
			r.remember(soulReplayWithoutImages(event))
		}
		at := time.Unix(tc.Event.Time, 0)
		r.now = func() time.Time { return at }
		before := len(channel.sentSnapshot())
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err := r.handleNotice(ctx, tc.Event)
		cancel()
		var texts []string
		for _, msg := range channel.sentSnapshot()[before:] {
			texts = append(texts, msg.Text)
		}
		sent := strings.Join(texts, " | ")
		t.Logf("group=%s name=%s sent=%s err=%v", tc.Event.GroupID, tc.Event.SenderName, sent, err)
		results = append(results, map[string]string{"group": tc.Event.GroupID, "name": tc.Event.SenderName, "welcome": sent})
		if err != nil || sent == "" || strings.Contains(sent, "[diana-") || strings.Contains(sent, "[[DIANA_") {
			t.Errorf("group %s: unusable welcome %q err=%v", tc.Event.GroupID, sent, err)
		}
	}
	if out := os.Getenv("DIANA_WELCOME_GENERATION_RESULTS"); out != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(out, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
