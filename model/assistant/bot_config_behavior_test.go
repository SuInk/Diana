package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 群里说「本群别按换行分条了，复读概率调到 50」要能真的改掉，而且只改本群。
func TestBotConfigBehaviorUpdatesGroupOverride(t *testing.T) {
	base := BotConfig{ID: "a", OwnerID: "owner", ReplyLineSplitEnabled: boolPointer(true), MaxReplyChars: 3500,
		RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 3, Probability: 30, CooldownSeconds: 60}}
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "owner"}
	tool := newDianaBotConfigTool(r, event)
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "update", "behavior": map[string]any{
		"reply_line_split_enabled": false, "random_repeat_probability": 50, "random_repeat_cooldown_seconds": -1, "max_reply_chars": 800,
	}})
	if err != nil {
		t.Fatal(err)
	}
	effective := r.effectiveConfigForEvent(event)
	if boolValue(effective.ReplyLineSplitEnabled, true) || effective.MaxReplyChars != 800 {
		t.Fatalf("group override not applied: split=%v chars=%d", effective.ReplyLineSplitEnabled, effective.MaxReplyChars)
	}
	if repeat := effective.RandomRepeat.normalized(); repeat.Probability != 50 || repeat.Threshold != 3 || repeat.CooldownSeconds != -1 || !repeat.Enabled {
		t.Fatalf("random repeat lost untouched fields: %+v", repeat)
	}
	if !boolValue(r.ProfileConfig("a").ReplyLineSplitEnabled, false) {
		t.Fatal("group update changed the bot default")
	}
	var response struct {
		Behavior struct {
			Values         map[string]any `json:"values"`
			GroupOverrides []string       `json:"group_overrides"`
		} `json:"behavior"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if response.Behavior.Values["reply_line_split_enabled"] != false || !strings.Contains(strings.Join(response.Behavior.GroupOverrides, ","), "max_reply_chars") {
		t.Fatalf("response = %s", raw)
	}

	// null 清掉本群覆盖，回到跟随机器人。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "behavior": map[string]any{"reply_line_split_enabled": nil, "random_repeat_enabled": nil}}); err != nil {
		t.Fatal(err)
	}
	group, _ := r.groupConfigForEvent(event)
	if group.ReplyLineSplitEnabled != nil || group.RandomRepeat != nil || group.MaxReplyChars != 800 {
		t.Fatalf("clear did not restore inheritance: %+v", group)
	}
}

func TestBotConfigBehaviorRejectsInvalidValues(t *testing.T) {
	base := BotConfig{ID: "a", OwnerID: "owner"}
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&testWritableGroupConfigStore{})
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "owner"}
	for _, behavior := range []map[string]any{
		{"unknown_switch": true},
		{"max_reply_chars": 1},
		{"random_repeat_cooldown_seconds": -2},
		{"typing_delay_enabled": "yes"},
		{"random_repeat_enabled": nil, "random_repeat_probability": 50},
		{},
	} {
		if _, err := newDianaBotConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "behavior": behavior}); err == nil {
			t.Fatalf("accepted invalid behavior %v", behavior)
		}
	}
	// 机器人级没有上一层，不能清空。
	private := MessageEvent{ProfileID: "a", Kind: EventKindPrivate, UserID: "owner"}
	if _, err := newDianaBotConfigTool(r, private).Run(context.Background(), map[string]any{"operation": "update", "behavior": map[string]any{"typing_delay_enabled": nil}}); err == nil {
		t.Fatal("bot scope accepted null")
	}
}

func TestBotConfigBehaviorUpdatesBotDefaults(t *testing.T) {
	cfg := BotConfig{ID: "b", OwnerID: "b-owner"}
	saver := &participationToolSaver{cfg: cfg}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "b", UserID: "b-owner"}
	if _, err := newDianaBotConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "behavior": map[string]any{"typing_delay_enabled": true, "random_repeat_enabled": true, "random_repeat_cooldown_seconds": -1}}); err != nil {
		t.Fatal(err)
	}
	if !boolValue(saver.cfg.TypingDelayEnabled, false) || saver.cfg.RandomRepeat == nil || !saver.cfg.RandomRepeat.Enabled || saver.cfg.RandomRepeat.CooldownSeconds != -1 {
		t.Fatalf("bot defaults not saved: %+v", saver.cfg)
	}
	event.UserID = "member"
	if _, err := newDianaBotConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "behavior": map[string]any{"typing_delay_enabled": false}}); err == nil {
		t.Fatal("non-owner changed bot defaults")
	}
}

// 未显式配置合并转发时，读取值必须与发送路径的默认行为一致。
func TestBotConfigBehaviorForwardDefaultMatchesSender(t *testing.T) {
	cfg := BotConfig{ForwardReplyThreshold: 1}
	values := behaviorSettingsView(cfg, nil)["values"].(map[string]any)
	if values["forward_reply_enabled"] != shouldUseForwardReplyFor(cfg, "一段超过阈值的回复", []string{"一段超过阈值的回复"}) {
		t.Fatalf("reported forward default differs from sender: %v", values["forward_reply_enabled"])
	}
}
