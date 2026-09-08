package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSharedPluginMigrationSelectsWholeConfigAndPreservesBackups(t *testing.T) {
	m := resolverManager(t)
	state, _ := m.Get(resolverPluginID)
	state.ProfileConfigMigrated = true
	state.SharedConfigMigrated = false
	state.ProfileSettings = map[string]map[string]any{
		"a": {resolverSettingDouyinCookie: "account-a", resolverSettingMaxImages: float64(3)},
		"b": {resolverSettingDouyinCookie: "account-b", resolverSettingMaxImages: float64(7)},
	}
	state.ProfileEnabled = map[string]bool{"a": false, "b": true}
	m.Restore(map[string]PluginState{resolverPluginID: state})
	if !m.MigrateProfileConfigurations([]BotConfig{{ID: "b"}, {ID: "a"}}) {
		t.Fatal("migration was not performed")
	}
	for _, profile := range []string{"a", "b", "new"} {
		_, values, _ := m.PluginForConfiguration(resolverPluginID, profile)
		if values.String(resolverSettingDouyinCookie, "") != "account-b" || values.Int(resolverSettingMaxImages, 0) != 7 {
			t.Fatalf("migration mixed settings: profile=%s values=%v", profile, values)
		}
	}
	saved := m.Snapshot()[resolverPluginID]
	if saved.ProfileSettings["a"][resolverSettingDouyinCookie] != "account-a" || saved.ProfileEnabled["a"] || !saved.ProfileEnabled["b"] || saved.SharedConfigSource != "b" {
		t.Fatal("migration lost backup, source or independent enabled state")
	}
	data, _ := json.Marshal(saved.Redacted())
	if strings.Contains(string(data), "account-") || strings.Contains(string(data), "profile_settings") {
		t.Fatal("backup credential leaked")
	}
	if m.MigrateProfileConfigurations([]BotConfig{{ID: "a"}, {ID: "b"}}) {
		t.Fatal("migration repeated when robot order changed")
	}
}
