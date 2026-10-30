package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBotConfigFieldsGroupScope(t *testing.T) {
	base := DefaultBotConfig()
	base.ID, base.Enabled, base.OwnerID = "a", true, "owner"
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "123", UserID: "owner"}
	run := func(input map[string]any) (string, error) {
		return newDianaBotConfigTool(r, event).Run(context.Background(), input)
	}
	if _, err := run(map[string]any{"operation": "update", "fields": map[string]any{"system_prompt": "本群说粤语", "max_context_tokens": 6000}}); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.ConfigForGroup("a", "123")
	if saved.SystemPrompt != "本群说粤语" || saved.MaxContextTokens != 6000 {
		t.Fatalf("fields not saved: prompt=%q tokens=%d", saved.SystemPrompt, saved.MaxContextTokens)
	}
	before, _ := json.Marshal(store.set)
	for _, patch := range []map[string]any{
		{"extension_access": map[string]any{}},
		{"group_id": "999"},
		{"no_such_key": 1},
		{"max_context_tokens": "很多"},
		{"proactive_reply_extra_criteria": strings.Repeat("长", ProactiveReplyExtraCriteriaMaxRunes+1)},
	} {
		if _, err := run(map[string]any{"operation": "update", "fields": patch}); err == nil {
			t.Fatalf("accepted %v", patch)
		}
	}
	if after, _ := json.Marshal(store.set); string(before) != string(after) {
		t.Fatal("rejected patch changed stored settings")
	}
	if _, err := run(map[string]any{"operation": "update", "fields": map[string]any{"system_prompt": nil}}); err != nil {
		t.Fatal(err)
	}
	if saved, _ = store.ConfigForGroup("a", "123"); saved.SystemPrompt != "" || saved.MaxContextTokens != 6000 {
		t.Fatalf("null did not clear only the named field: %+v", saved)
	}
	raw, err := run(map[string]any{"operation": "get", "list_fields": true})
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Fields map[string]any `json:"fields"`
	}
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Fields["max_context_tokens"]; !ok {
		t.Fatal("list_fields missing editable key")
	}
	if _, ok := view.Fields["extension_access"]; ok {
		t.Fatal("list_fields exposed denied key")
	}
	member := event
	member.UserID = "member"
	if _, err := newDianaBotConfigTool(r, member).Run(context.Background(), map[string]any{"operation": "update", "fields": map[string]any{"max_reply_chars": 50}}); err == nil {
		t.Fatal("member changed group fields")
	}
}

func TestBotConfigFieldsBotScope(t *testing.T) {
	b := DefaultBotConfig()
	b.ID, b.Enabled, b.OwnerID, b.OneBotAccessToken = "b", true, "b-owner", "keep-me"
	saver := &participationToolSaver{cfg: b}
	r := NewRuntime(b, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{b}})
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "b", UserID: "b-owner"}
	run := func(input map[string]any) (string, error) {
		return newDianaBotConfigTool(r, event).Run(context.Background(), input)
	}
	if _, err := run(map[string]any{"operation": "update", "fields": map[string]any{"mood_enabled": false, "system_prompt": "新人设"}}); err != nil {
		t.Fatal(err)
	}
	if saver.cfg.SystemPrompt != "新人设" || saver.cfg.MoodEnabled == nil || *saver.cfg.MoodEnabled {
		t.Fatalf("bot fields not saved: prompt=%q mood=%v", saver.cfg.SystemPrompt, saver.cfg.MoodEnabled)
	}
	if saver.cfg.OneBotAccessToken != "keep-me" || saver.cfg.OwnerID != "b-owner" {
		t.Fatal("bot field patch touched credentials or owner")
	}
	for _, patch := range []map[string]any{
		{"owner_id": "attacker"},
		{"admin_ids": []any{"attacker"}},
		{"onebot_access_token": "x"},
		{"agent_desktop_control_enabled": true},
		{"reply_account_safety_audit_master_enabled": false},
		{"enabled": false},
		{"nope": true},
	} {
		if _, err := run(map[string]any{"operation": "update", "fields": patch}); err == nil {
			t.Fatalf("accepted %v", patch)
		}
	}
	raw, err := run(map[string]any{"operation": "get", "list_fields": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "keep-me") || strings.Contains(raw, "\"owner_id\"") {
		t.Fatal("list_fields exposed credential or owner field")
	}
	event.UserID = "stranger"
	if _, err := run(map[string]any{"operation": "update", "fields": map[string]any{"mood_enabled": true}}); err == nil {
		t.Fatal("non-owner changed bot fields")
	}
}
