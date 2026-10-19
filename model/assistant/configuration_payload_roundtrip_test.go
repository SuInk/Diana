// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigPayloadKeepsProactiveReplyChance(t *testing.T) {
	cfg := ConfigFromPayload(ConfigPayload{
		Enabled:                 true,
		ProactiveReplyChance:    0.4,
		ProactiveReplyThreshold: 0.92,
	}, BotConfig{})
	if cfg.ProactiveReplyChance != 0.4 {
		t.Fatalf("ProactiveReplyChance = %v", cfg.ProactiveReplyChance)
	}
	payload := PayloadFromConfig(cfg)
	if payload.ProactiveReplyChance != 0.4 {
		t.Fatalf("payload ProactiveReplyChance = %v", payload.ProactiveReplyChance)
	}
	if cfg.ProactiveReplyThreshold != 0.92 || payload.ProactiveReplyThreshold != 0.92 {
		t.Fatalf("threshold cfg=%v payload=%v", cfg.ProactiveReplyThreshold, payload.ProactiveReplyThreshold)
	}
}

func TestConfigPayloadKeepsEditablePrompts(t *testing.T) {
	cfg := ConfigFromPayload(ConfigPayload{
		Enabled:      true,
		SystemPrompt: "custom system prompt",
		PromptOverrides: PromptOverrides{
			promptLegacyRouterSpec.Key:   "custom router prompt",
			promptProactiveReplySpec.Key: "custom proactive reply prompt",
		},
	}, BotConfig{}).WithDefaults()
	payload := PayloadFromConfig(cfg)

	if payload.SystemPrompt != "custom system prompt" {
		t.Fatalf("SystemPrompt = %q", payload.SystemPrompt)
	}
	if got := payload.PromptOverrides[promptLegacyRouterSpec.Key]; got != "custom router prompt" {
		t.Fatalf("router override = %q", got)
	}
	if got := payload.PromptOverrides[promptProactiveReplySpec.Key]; got != "custom proactive reply prompt" {
		t.Fatalf("proactive reply override = %q", got)
	}
}

// Removed bridge settings in existing databases must not break configuration loading
// or reappear in exported configuration, including the old bridge credential.
func TestConfigIgnoresRemovedBridgeSettings(t *testing.T) {
	raw := []byte(`{"name":"Diana","onebot_access_token":"onebot-credential","nonebot_bridge_enabled":true,"nonebot_bridge_endpoint":"ws://127.0.0.1:8080/onebot/v11/ws","nonebot_bridge_token":"retired-bridge-credential"}`)
	var cfg BotConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithDefaults()
	if cfg.Name != "Diana" || cfg.OneBotAccessToken != "onebot-credential" {
		t.Fatal("loading legacy settings changed the active connection")
	}
	for _, value := range []any{cfg, PayloadFromConfig(cfg)} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "nonebot_bridge") || strings.Contains(string(encoded), "retired-bridge-credential") {
			t.Fatal("removed bridge settings survived round trip")
		}
	}
}
