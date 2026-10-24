package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestBotConfigGroupSettingsScopeInheritanceAndValidation(t *testing.T) {
	base := BotConfig{ID: "a", Enabled: true, OwnerID: "owner", GroupTriggers: []string{"Diana"}}
	plugins := NewPluginManager()
	if err := plugins.RegisterPlugin(testPlugin{}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install("test"); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(base, nilChannel{}, plugins, nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "123", UserID: "owner"}
	tool := newDianaBotConfigTool(r, event)
	run := func(patch map[string]any) (string, error) {
		return tool.Run(context.Background(), map[string]any{"operation": "update", "group_settings": patch})
	}
	raw, err := run(map[string]any{"enabled": false, "triggers": []any{"然然"}, "trigger_mode": "strict", "plugins": map[string]any{"test": false}})
	if err != nil {
		t.Fatal(err)
	}
	group, _ := store.ConfigForGroup("a", "123")
	if group.Enabled || !group.EnabledSet || !reflect.DeepEqual(group.GroupTriggers, []string{"然然"}) || group.GroupTriggerMode != AliasTriggerStrict || group.PluginOverrides["test"] {
		t.Fatalf("wrong saved settings: %+v", group)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if response["group_settings"].(map[string]any)["enabled"] != false {
		t.Fatal("did not return saved state")
	}
	if len(store.set.Groups) != 1 || r.ProfileConfig("a").GroupTriggers[0] != "Diana" {
		t.Fatal("update escaped group scope")
	}
	before, _ := json.Marshal(store.set)
	for _, patch := range []map[string]any{
		{"plugins": map[string]any{"test": true, "missing": false}},
		{"plugins": map[string]any{webSearchPluginID: false}},
		{"plugins": map[string]any{"test": "false"}},
		{"trigger_mode": "invalid"}, {"triggers": []any{42}}, {"enabled": "false"}, {"unknown": true},
	} {
		if _, err := run(patch); err == nil {
			t.Fatalf("accepted %v", patch)
		}
		after, _ := json.Marshal(store.set)
		if string(before) != string(after) {
			t.Fatalf("failed patch changed stored settings: %v", patch)
		}
	}
	if _, err := run(map[string]any{"enabled": true, "triggers": []any{}, "trigger_mode": "", "plugins": map[string]any{"test": nil}}); err != nil {
		t.Fatal(err)
	}
	group, _ = store.ConfigForGroup("a", "123")
	if !group.Enabled || len(group.PluginOverrides) != 0 || len(group.GroupTriggers) != 0 || group.GroupTriggerMode != "" {
		t.Fatalf("inheritance not restored: %+v", group)
	}
	if got := r.effectiveConfigForEvent(event).GroupTriggers; !reflect.DeepEqual(got, []string{"Diana"}) {
		t.Fatalf("effective triggers %v", got)
	}
	event.UserID = "member"
	if _, err := newDianaBotConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "group_settings": map[string]any{"enabled": false}}); err == nil {
		t.Fatal("member changed group settings")
	}
	event.UserID, event.Kind = "owner", EventKindPrivate
	if _, err := newDianaBotConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "scope": "bot", "group_settings": map[string]any{"enabled": false}}); err == nil {
		t.Fatal("bot scope accepted group patch")
	}
}
