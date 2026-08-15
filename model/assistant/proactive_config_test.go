package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBotConfigMigratesLegacyProactiveReplyFields(t *testing.T) {
	var cfg BotConfig
	err := json.Unmarshal([]byte(`{
		"passive_reply_router_prompt":"legacy router",
		"passive_reply_prompt":"legacy reply",
		"passive_reply_chance":0.42,
		"passive_reply_threshold":0.8
	}`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithDefaults()
	if cfg.ProactiveReplyRouterPrompt != "legacy router" || cfg.ProactiveReplyPrompt != "legacy reply" {
		t.Fatalf("prompts = %q / %q", cfg.ProactiveReplyRouterPrompt, cfg.ProactiveReplyPrompt)
	}
	if cfg.ProactiveReplyChance != 0.42 || cfg.ProactiveReplyThreshold != 0.9 {
		t.Fatalf("proactive settings = %v / %v", cfg.ProactiveReplyChance, cfg.ProactiveReplyThreshold)
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithDefaults()
	if strings.Contains(string(encoded), "passive_reply") || !strings.Contains(string(encoded), "proactive_reply_threshold") {
		t.Fatalf("migrated JSON = %s", encoded)
	}
}

func TestProactiveReplyJSONNamesTakePriorityOverLegacyNames(t *testing.T) {
	var cfg BotConfig
	err := json.Unmarshal([]byte(`{
		"proactive_reply_router_prompt":"new router",
		"proactive_reply_prompt":"new reply",
		"proactive_reply_chance":0.75,
		"proactive_reply_threshold":0.95,
		"passive_reply_router_prompt":"old router",
		"passive_reply_prompt":"old reply",
		"passive_reply_chance":0.25,
		"passive_reply_threshold":0.6
	}`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProactiveReplyRouterPrompt != "new router" || cfg.ProactiveReplyPrompt != "new reply" || cfg.ProactiveReplyChance != 0.75 || cfg.ProactiveReplyThreshold != 0.95 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestGroupAndPayloadMigrateLegacyProactiveReplyFields(t *testing.T) {
	var group GroupConfig
	if err := json.Unmarshal([]byte(`{"group_id":"123","passive_reply_chance":0.4,"passive_reply_threshold":0.93}`), &group); err != nil {
		t.Fatal(err)
	}
	group = group.WithDefaults("123", DefaultBotConfig())
	if group.ProactiveReplyChance != 0.4 || group.ProactiveReplyThreshold != 0.93 {
		t.Fatalf("group = %#v", group)
	}

	var payload ConfigPayload
	if err := json.Unmarshal([]byte(`{"passive_reply_router_prompt":"route","passive_reply_prompt":"reply","passive_reply_chance":0.5,"passive_reply_threshold":0.91}`), &payload); err != nil {
		t.Fatal(err)
	}
	cfg := ConfigFromPayload(payload, BotConfig{})
	if cfg.ProactiveReplyRouterPrompt != "route" || cfg.ProactiveReplyPrompt != "reply" || cfg.ProactiveReplyChance != 0.5 || cfg.ProactiveReplyThreshold != 0.91 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestGroupConfigSetNormalizesLegacyFieldsBeforeSave(t *testing.T) {
	legacyChance := 0.4
	legacyThreshold := 0.8
	set := GroupConfigSet{Groups: []GroupConfig{{
		GroupID:                     "123",
		Enabled:                     true,
		EnabledSet:                  true,
		LegacyPassiveReplyChance:    &legacyChance,
		LegacyPassiveReplyThreshold: &legacyThreshold,
	}}}.WithDefaults(DefaultBotConfig())
	if len(set.Groups) != 1 || set.Groups[0].ProactiveReplyChance != 0.4 || set.Groups[0].ProactiveReplyThreshold != 0.9 {
		t.Fatalf("groups = %#v", set.Groups)
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "passive_reply") {
		t.Fatalf("normalized JSON = %s", encoded)
	}
}
