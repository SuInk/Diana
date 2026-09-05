// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"testing"
)

func TestCrossPlatformMemoryConfigRoundTrip(t *testing.T) {
	if boolValue(DefaultBotConfig().CrossPlatformMemoryEnabled, true) {
		t.Fatal("cross-platform sharing must be opt-in")
	}
	for _, enabled := range []bool{false, true} {
		cfg := DefaultBotConfig()
		cfg.CrossPlatformMemoryEnabled = boolPointer(enabled)
		payload := PayloadFromConfig(cfg)
		restored := ConfigFromPayload(payload, DefaultBotConfig()).WithDefaults()
		if boolValue(restored.CrossPlatformMemoryEnabled, !enabled) != enabled {
			t.Fatalf("cross-platform flag lost on save: %v", enabled)
		}
	}
}

func TestCrossPlatformMemoryRequiresMutualOptIn(t *testing.T) {
	profile := func(id, platform string, enabled bool) BotConfig {
		cfg := DefaultBotConfig()
		cfg.ID, cfg.Platform, cfg.Enabled = id, platform, true
		cfg.CrossPlatformMemoryEnabled = boolPointer(enabled)
		return cfg
	}
	target := profile("qq", PlatformOneBotV11, true)
	source := profile("tg", PlatformTelegram, true)
	other := profile("off", PlatformTelegram, false)
	samePlatform := profile("qq2", PlatformOneBotV11, true)
	r := NewRuntime(target, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	set := ProfileSet{ActiveID: target.ID, Profiles: []BotConfig{target, source, other, samePlatform}}
	r.SetProfiles(set)
	event := MessageEvent{Kind: EventKindGroup, Platform: target.Platform, ProfileID: target.ID, ContextNamespace: target.ID, GroupID: "1", UserID: "123"}
	if got := r.crossPlatformMemoryPrefixes(event, target); !reflect.DeepEqual(got, []string{"tg:group:"}) {
		t.Fatalf("eligible namespaces=%v", got)
	}
	for _, test := range []string{"target-off", "source-off", "source-disabled", "source-memory-off", "target-memory-off", "private", "unisolated"} {
		t.Run(test, func(t *testing.T) {
			cfg, sourceCfg, next := target, source, event
			switch test {
			case "target-off":
				cfg.CrossPlatformMemoryEnabled = boolPointer(false)
			case "source-off":
				sourceCfg.CrossPlatformMemoryEnabled = boolPointer(false)
			case "source-disabled":
				sourceCfg.Enabled = false
			case "source-memory-off":
				sourceCfg.LongTermMemoryEnabled = boolPointer(false)
			case "target-memory-off":
				cfg.LongTermMemoryEnabled = boolPointer(false)
			case "private":
				next.Kind = EventKindPrivate
			case "unisolated":
				next.ContextNamespace = ""
			}
			r.SetProfiles(ProfileSet{ActiveID: target.ID, Profiles: []BotConfig{target, sourceCfg}})
			if got := r.crossPlatformMemoryPrefixes(next, cfg); len(got) != 0 {
				t.Fatalf("unexpected sharing: %v", got)
			}
		})
	}
}
