package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBotConfigPluginSettingsGroupAndBotScope(t *testing.T) {
	base := BotConfig{ID: "a", Enabled: true, OwnerID: "owner"}
	plugins := NewPluginManager()
	if err := plugins.RegisterPlugin(&settingsProbePlugin{}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install("probe"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.UpdateSettings("probe", map[string]any{"token": "secret-value"}); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(base, nilChannel{}, plugins, nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	persisted := 0
	r.SetPluginStatePersister(func(context.Context) error { persisted++; return nil })
	group := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "123", UserID: "owner"}
	run := func(event MessageEvent, input map[string]any) (string, error) {
		return newDianaBotConfigTool(r, event).Run(context.Background(), input)
	}

	// 群级：写覆盖，不动全局。
	if _, err := run(group, map[string]any{"operation": "update", "plugin_settings": map[string]any{"probe": map[string]any{"limit": 7}}}); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.ConfigForGroup("a", "123")
	if saved.PluginSettingOverrides["probe"]["limit"] == nil {
		t.Fatalf("group override not saved: %+v", saved.PluginSettingOverrides)
	}
	if state, _ := plugins.Get("probe"); state.Settings["limit"] != nil {
		t.Fatal("group update leaked into global settings")
	}
	raw, err := run(group, map[string]any{"operation": "get", "plugin": "probe"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "secret-value") {
		t.Fatal("get leaked secret value")
	}
	var view struct {
		PluginSettings struct {
			Settings []map[string]any `json:"settings"`
		} `json:"plugin_settings"`
	}
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	for _, item := range view.PluginSettings.Settings {
		switch item["key"] {
		case "limit":
			if item["value"] != float64(7) {
				t.Fatalf("effective limit = %v", item["value"])
			}
		case "token":
			if item["secret_configured"] != true || item["editable"] != false {
				t.Fatalf("secret view = %v", item)
			}
		}
	}

	// 拒绝：凭据、未知键、类型错误、未知插件；失败不改存储。
	before, _ := json.Marshal(store.set)
	for _, patch := range []map[string]any{
		{"probe": map[string]any{"token": "x"}},
		{"probe": map[string]any{"missing": 1}},
		{"probe": map[string]any{"limit": "many"}},
		{"nope": map[string]any{"limit": 1}},
	} {
		if _, err := run(group, map[string]any{"operation": "update", "plugin_settings": patch}); err == nil {
			t.Fatalf("accepted %v", patch)
		}
	}
	if after, _ := json.Marshal(store.set); string(before) != string(after) {
		t.Fatal("rejected patch changed stored settings")
	}

	// null 恢复继承。
	if _, err := run(group, map[string]any{"operation": "update", "plugin_settings": map[string]any{"probe": map[string]any{"limit": nil}}}); err != nil {
		t.Fatal(err)
	}
	if saved, _ = store.ConfigForGroup("a", "123"); len(saved.PluginSettingOverrides) != 0 {
		t.Fatalf("override not cleared: %+v", saved.PluginSettingOverrides)
	}

	// 群成员不能改。
	member := group
	member.UserID = "member"
	if _, err := run(member, map[string]any{"operation": "update", "plugin_settings": map[string]any{"probe": map[string]any{"limit": 2}}}); err == nil {
		t.Fatal("member changed plugin settings")
	}

	// 机器人级：主人私聊改全局，保留凭据并落盘。
	private := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "owner"}
	if _, err := run(private, map[string]any{"operation": "update", "plugin_settings": map[string]any{"probe": map[string]any{"verbose": false}}}); err != nil {
		t.Fatal(err)
	}
	state, _ := plugins.Get("probe")
	if state.Settings["verbose"] != false || state.Settings["token"] != "secret-value" {
		t.Fatalf("bot settings = %+v", state.Settings)
	}
	if persisted != 1 {
		t.Fatalf("persisted %d times", persisted)
	}
	private.UserID = "stranger"
	if _, err := run(private, map[string]any{"operation": "update", "plugin_settings": map[string]any{"probe": map[string]any{"verbose": true}}}); err == nil {
		t.Fatal("non-owner changed bot plugin settings")
	}
}
