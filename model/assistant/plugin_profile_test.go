package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPluginProfileSwitchIsolationAndRestore(t *testing.T) {
	m := NewDefaultPluginManager()
	id := statusCommandPluginID
	if _, err := m.SetEnabledForProfile(id, "qq-a", true); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"qq-a", "qq-b", "tg", ""} {
		if got := m.EnabledWithOverrides(id, m.ProfileOverrides(profile)); got != (profile == "qq-a") {
			t.Fatalf("profile %q enabled = %v", profile, got)
		}
	}
	data, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]PluginState
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	restored := NewDefaultPluginManager()
	restored.Restore(saved)
	saved[id].ProfileEnabled["qq-a"] = false
	if !restored.EnabledWithOverrides(id, restored.ProfileOverrides("qq-a")) || restored.Enabled(id) {
		t.Fatal("restore lost isolation or retained a mutable input map")
	}
	snapshot := restored.Snapshot()
	if _, err := restored.SetEnabledForProfile(id, "qq-a", false); err != nil {
		t.Fatal(err)
	}
	if !snapshot[id].ProfileEnabled["qq-a"] {
		t.Fatal("updating profile mutated a persistence snapshot")
	}
	if _, err := restored.SetEnabledForProfile(messageHistoryPluginID, "qq-a", false); !errors.Is(err, ErrInternalPluginDisable) {
		t.Fatalf("internal plugin disable: %v", err)
	}
	if _, err := restored.SetEnabledForProfile(OpenAPIPluginID, "qq-a", true); err != nil || !restored.Enabled(OpenAPIPluginID) {
		t.Fatalf("OpenAPI must remain process-wide: %v", err)
	}
}

func TestRuntimePluginSwitchUsesEventProfileAndGroupOverride(t *testing.T) {
	m := NewDefaultPluginManager()
	r := NewRuntime(BotConfig{ID: "qq-a"}, nilChannel{}, m, nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{ActiveID: "qq-a", Profiles: []BotConfig{
		{ID: "qq-a", Platform: PlatformOneBotV11},
		{ID: "qq-b", Platform: PlatformOneBotV11},
		{ID: "tg", Platform: PlatformTelegram},
	}})
	if _, err := m.SetEnabledForProfile(statusCommandPluginID, "qq-a", true); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"qq-a", "qq-b", "tg", ""} {
		for _, kind := range []EventKind{EventKindPrivate, EventKindGroup} {
			event := MessageEvent{ProfileID: profile, Kind: kind, GroupID: "same-group"}
			_, _, enabled := r.pluginWithSettingsForEvent(statusCommandPluginID, event)
			if enabled != (profile == "qq-a" || profile == "") {
				t.Fatalf("profile %q kind %q enabled = %v", profile, kind, enabled)
			}
			overrides := r.pluginOverridesForEvent(event)
			if got := m.ShouldHandleWithOverrides(event, "#diana", overrides); got != enabled {
				t.Fatalf("profile %q direct trigger = %v", profile, got)
			}
			response, err := m.RunOneWithOverrides(context.Background(), statusCommandPluginID, PluginRequest{Event: event, Text: "#diana"}, overrides)
			if err != nil || (response != nil) != enabled {
				t.Fatalf("profile %q dispatch response=%v err=%v", profile, response, err)
			}
		}
	}
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"same-group": {GroupID: "same-group", PluginOverrides: map[string]bool{statusCommandPluginID: false}},
	}})
	_, _, enabled := r.pluginWithSettingsForEvent(statusCommandPluginID, MessageEvent{ProfileID: "qq-a", Kind: EventKindGroup, GroupID: "same-group"})
	if enabled {
		t.Fatal("group override must take precedence over the profile")
	}
}

func TestPluginProfileSwitchFiltersAgentTools(t *testing.T) {
	m := NewPluginManager(&echoAgentToolPlugin{tool: &echoAgentTool{}})
	r := NewRuntime(BotConfig{ID: "qq"}, nilChannel{}, m, nil, nil, nil, nil)
	if _, err := m.SetEnabledForProfile("test.echo-tool", "qq", false); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"qq", "tg"} {
		tools, err := m.AgentToolsWithOverrides(r.pluginOverridesForEvent(MessageEvent{ProfileID: profile}))
		if err != nil || (len(tools) > 0) != (profile == "tg") {
			t.Fatalf("profile %q tools=%v err=%v", profile, tools, err)
		}
	}
}
