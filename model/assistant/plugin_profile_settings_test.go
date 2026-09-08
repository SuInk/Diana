package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOrdinaryPluginSettingsAndSecretsAreShared(t *testing.T) {
	m := resolverManager(t)
	if _, err := m.UpdateSettings(resolverPluginID, map[string]any{resolverSettingDouyinCookie: "legacy-secret", resolverSettingMaxImages: 4}); err != nil {
		t.Fatal(err)
	}
	m.MigrateProfileConfigurations([]BotConfig{{ID: "a"}, {ID: "b"}})
	if _, err := m.UpdateSettingsForProfile(resolverPluginID, "a", map[string]any{resolverSettingMaxImages: 6}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateSettingsForProfile(resolverPluginID, "b", map[string]any{resolverSettingDouyinCookie: "b-secret", resolverSettingMaxImages: 8}, nil); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(BotConfig{ID: "a"}, nilChannel{}, m, nil, nil, nil, nil)
	for _, tc := range []struct {
		id, cookie string
		images     int
	}{{"a", "b-secret", 8}, {"b", "b-secret", 8}, {"unconfigured", "b-secret", 8}} {
		_, settings, enabled := r.pluginWithSettingsForEvent(resolverPluginID, MessageEvent{ProfileID: tc.id})
		if !enabled || settings.String(resolverSettingDouyinCookie, "") != tc.cookie || settings.Int(resolverSettingMaxImages, 0) != tc.images {
			t.Fatalf("profile %s mixed settings", tc.id)
		}
	}
	if _, err := m.UpdateSettingsForProfile(resolverPluginID, "a", map[string]any{resolverSettingMaxImages: 2}, []string{resolverSettingDouyinCookie}); err != nil {
		t.Fatal(err)
	}
	state, _ := m.Get(resolverPluginID)
	if state.ForProfile("a").Settings[resolverSettingDouyinCookie] != nil || state.ForProfile("b").Settings[resolverSettingDouyinCookie] != nil {
		t.Fatal("shared secret clear did not reach every robot")
	}
	for _, profile := range []string{"a", "b", ""} {
		encoded, _ := json.Marshal(state.ForProfile(profile).Redacted())
		for _, secret := range []string{"legacy-secret", "b-secret", "new-default", "profile_settings"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("response leaked another profile or secret")
			}
		}
	}
	encoded, _ := json.Marshal(m.Snapshot())
	var saved map[string]PluginState
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	restored := resolverManager(t)
	restored.Restore(saved)
	if restored.MigrateProfileConfigurations([]BotConfig{{ID: "a"}, {ID: "b"}, {ID: "new"}}) {
		t.Fatal("migration ran twice")
	}
	_, fresh, _ := restored.PluginWithSettingsForProfile(resolverPluginID, "new")
	if fresh.String(resolverSettingDouyinCookie, "") != "" {
		t.Fatal("new bot inherited old credential")
	}
	saved[resolverPluginID].Settings[resolverSettingDouyinCookie] = "mutated"
	state, _ = restored.Get(resolverPluginID)
	if state.ForProfile("b").Settings[resolverSettingDouyinCookie] != nil || state.ForProfile("a").Settings[resolverSettingDouyinCookie] != nil {
		t.Fatal("restore lost isolated secret state")
	}
}

func TestProfilePluginSettingsAllowGroupOverrides(t *testing.T) {
	m := resolverManager(t)
	if _, err := m.UpdateSettingsForProfile(resolverPluginID, "a", map[string]any{resolverSettingMaxImages: 6}, nil); err != nil {
		t.Fatal(err)
	}
	overrides := PluginSettingOverrides{pluginSettingsProfileKey: {"profile_id": "a"}, resolverPluginID: {resolverSettingMaxImages: 2}}
	_, settings, ok := m.PluginWithSettingsForGroup(resolverPluginID, nil, overrides)
	if !ok || settings.Int(resolverSettingMaxImages, 0) != 2 {
		t.Fatal("group override no longer wins")
	}
	state, _ := m.Get(resolverPluginID)
	if state.ForProfile("a").Settings[resolverSettingMaxImages] != float64(6) {
		t.Fatal("group override mutated profile settings")
	}
}

func TestPluginMigrationPreservesIndependentSettingsAndSwitches(t *testing.T) {
	m := resolverManager(t)
	if _, err := m.UpdateSettings(resolverPluginID, map[string]any{resolverSettingDouyinCookie: "legacy", resolverSettingMaxImages: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(resolverPluginID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateSettingsForProfile(resolverPluginID, "a", map[string]any{resolverSettingDouyinCookie: "independent", resolverSettingMaxImages: 7}, nil); err != nil {
		t.Fatal(err)
	}
	if m.MigrateProfileConfigurations([]BotConfig{{ID: ""}}) {
		t.Fatal("empty identities consumed migration")
	}
	if !m.MigrateProfileConfigurations([]BotConfig{{ID: "a"}, {ID: "b"}}) {
		t.Fatal("migration not performed")
	}
	state, _ := m.Get(resolverPluginID)
	if state.ForProfile("a").Settings[resolverSettingDouyinCookie] != "independent" || state.ForProfile("b").Settings[resolverSettingDouyinCookie] != "independent" {
		t.Fatal("migration overwrote or retained shared settings")
	}
	if state.ForProfile("a").Enabled || state.ForProfile("b").Enabled || !state.ForProfile("new").Enabled {
		t.Fatal("old switches or new defaults incorrect")
	}
	if _, err := m.UpdateSettings(resolverPluginID, map[string]any{}); err != nil {
		t.Fatal("shared writes failed after migration")
	}
	if _, err := m.SetEnabled(resolverPluginID, true); err == nil {
		t.Fatal("global toggles allowed after migration")
	}
}

func TestNewRobotUsesMigratedSharedCredentials(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "environment-cookie")
	t.Setenv("DIANA_YTDLP_COOKIES_FROM_BROWSER", "chrome")
	m := resolverManager(t)
	m.MigrateProfileConfigurations([]BotConfig{{ID: "old"}})
	_, old, _ := m.PluginWithSettingsForProfile(resolverPluginID, "old")
	if old.String(resolverSettingDouyinCookie, "") != "environment-cookie" {
		t.Fatal("old deployment credential not migrated")
	}
	_, fresh, _ := m.PluginWithSettingsForProfile(resolverPluginID, "new")
	ctx := withResolverCredentials(context.Background(), resolverCredentialsFromSettings(fresh))
	if resolverDouyinCookie(ctx) != "environment-cookie" {
		t.Fatal("new robot used process credential")
	}
	args := appendYTDLPResolverArgs(ctx, nil, "https://example.com/video")
	if !strings.Contains(strings.Join(args, " "), "cookies") {
		t.Fatal("new robot used shared browser credentials")
	}
	ctx = withResolverCredentials(context.Background(), resolverCredentialsFromSettings(old))
	if resolverDouyinCookie(ctx) != "environment-cookie" {
		t.Fatal("old robot lost migrated credential")
	}
}

func TestMigrationSeedsSharedSettingsWithoutExistingBots(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "old-process-cookie")
	m := resolverManager(t)
	if !m.MigrateProfileConfigurations(nil) {
		t.Fatal("empty deployment migration not completed")
	}
	if m.MigrateProfileConfigurations([]BotConfig{{ID: "new"}}) {
		t.Fatal("future robot retriggered migration")
	}
	_, settings, _ := m.PluginWithSettingsForProfile(resolverPluginID, "new")
	if settings.String(resolverSettingDouyinCookie, "") != "old-process-cookie" {
		t.Fatal("future robot inherited environment")
	}
}
