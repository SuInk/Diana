package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBotConfigWelcomeGroupUpdatesDoNotCreateTasksOrChangeParticipation(t *testing.T) {
	base := BotConfig{ID: "a", Enabled: true, OwnerID: "owner", BotAccount: "bot", WelcomeMessage: "欢迎 {user_id}",
		Participation: &ParticipationPreferences{Desire: 75, RelevanceLevel: "on", ChatLevel: "high", CooldownSeconds: 120}}
	channel := &recordingChannel{}
	tasks := &stubReminderStore{}
	r := NewRuntime(base, channel, NewPluginManager(), nil, tasks, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base, {ID: "b", OwnerID: "other"}}})
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "123", UserID: "owner"}
	tool := newDianaBotConfigTool(r, event)
	before := r.effectiveConfigForEvent(event).participationPreferences()
	for i := 0; i < 2; i++ {
		raw, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_enabled": true})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Welcome welcomeSettings `json:"welcome"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil || !result.Welcome.Enabled || result.Welcome.Message != "欢迎 {user_id}" {
			t.Fatalf("effective welcome response = %s, error = %v", raw, err)
		}
	}
	if len(tasks.Reminders()) != 0 || len(store.Groups().Groups) != 1 {
		t.Fatal("repeated welcome update created tasks or duplicate group settings")
	}
	group, _ := store.ConfigForGroup("a", "123")
	if group.Participation != nil || group.ResponseMode != "" || !reflect.DeepEqual(before, r.effectiveConfigForEvent(event).participationPreferences()) {
		t.Fatal("welcome-only update changed participation or broke inheritance")
	}
	for _, other := range []MessageEvent{{ProfileID: "b", Kind: EventKindGroup, GroupID: "123"}, {ProfileID: "a", Kind: EventKindGroup, GroupID: "456"}} {
		if r.effectiveConfigForEvent(other).WelcomeEnabled {
			t.Fatal("welcome update escaped its bot/group scope")
		}
	}
	data, err := json.Marshal(store.set)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &store.set); err != nil {
		t.Fatal(err)
	}
	join := event
	join.Kind, join.SubType, join.UserID = EventKindNotice, "group_increase", "new-member"
	if err := r.HandleEvent(context.Background(), join); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].GroupID != "123" || channel.sent[0].Text != "欢迎 new-member" {
		t.Fatalf("single member join sent = %#v", channel.sent)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_enabled": false}); err != nil {
		t.Fatal(err)
	}
	join.UserID = "another-member"
	if err := r.HandleEvent(context.Background(), join); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || r.effectiveConfigForEvent(event).WelcomeEnabled {
		t.Fatal("disabled group welcome still sent a message")
	}
}

func TestBotConfigWelcomeRejectsInvalidOrFailedUpdatesAtomically(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &failedGroupPolicyStore{}
	r.SetGroupConfigStore(store)
	tool := newDianaBotConfigTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "owner"})
	for _, input := range []map[string]any{
		{"welcome_enabled": "false"},
		{"welcome_message": 42},
		{"welcome_mode": "unknown"},
		{"welcome_templates": []any{"valid", 42}},
		{"welcome_templates": []string{strings.Repeat("字", 201)}},
		{"welcome_llm_cooldown_seconds": -1},
		{"welcome_llm_cooldown_seconds": 86401},
		{"welcome_llm_cooldown_seconds": 1.5},
	} {
		input["operation"], input["desire_level"] = "update", "off"
		if _, err := tool.Run(context.Background(), input); err == nil || strings.Contains(err.Error(), "disk full") {
			t.Fatalf("invalid input reached persistence: %v, error = %v", input, err)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_enabled": true, "desire_level": "off"}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatal("failed mixed update reported success")
	}
	if len(store.set.Groups) != 0 || r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "123"}).WelcomeEnabled {
		t.Fatal("failed update changed active settings")
	}
}

func TestBotConfigWelcomePartialBotUpdatePreservesParticipationAndFailsSafely(t *testing.T) {
	a := BotConfig{ID: "a", OwnerID: "a-owner"}
	b := BotConfig{ID: "b", OwnerID: "b-owner", WelcomeMessage: "原欢迎词", WelcomeMode: WelcomeModeLLM, WelcomeLLMCooldownSeconds: 30,
		Participation: &ParticipationPreferences{Desire: 75, CooldownSeconds: 120}}
	saver := &participationToolSaver{cfg: b}
	r := NewRuntime(a, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{a, b}})
	event := MessageEvent{ProfileID: "b", Kind: EventKindPrivate, UserID: "b-owner"}
	tool := newDianaBotConfigTool(r, event)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_enabled": true}); err != nil {
		t.Fatal(err)
	}
	actual := r.ProfileConfig("b")
	if !actual.WelcomeEnabled || actual.WelcomeMessage != "原欢迎词" || actual.WelcomeMode != WelcomeModeLLM || actual.WelcomeLLMCooldownSeconds != 30 || actual.Participation.Desire != 75 || r.ProfileConfig("a").WelcomeEnabled {
		t.Fatal("partial bot welcome update lost settings or changed another bot")
	}
	saver.fail = true
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_enabled": false, "desire_level": "off"}); err == nil {
		t.Fatal("failed mixed bot update reported success")
	}
	if !r.ProfileConfig("b").WelcomeEnabled || r.ProfileConfig("b").Participation.Desire != 75 {
		t.Fatal("failed mixed bot update changed runtime")
	}
}

func TestBotConfigWelcomeFieldsUseEffectiveGroupSettings(t *testing.T) {
	base := BotConfig{ID: "a", OwnerID: "owner", WelcomeMode: WelcomeModeFixed, WelcomeMessage: "默认欢迎", WelcomeLLMCooldownSeconds: 30}
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&testWritableGroupConfigStore{})
	event := MessageEvent{ProfileID: "a", Kind: EventKindGroup, GroupID: "123", UserID: "owner"}
	tool := newDianaBotConfigTool(r, event)
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_mode": "template", "welcome_message": "回落欢迎", "welcome_templates": []any{" 模板一 ", "模板二"}, "welcome_llm_cooldown_seconds": 60})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Welcome welcomeSettings `json:"welcome"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Welcome.Mode != WelcomeModeTemplate || result.Welcome.Message != "回落欢迎" || !reflect.DeepEqual(result.Welcome.Templates, []string{"模板一", "模板二"}) || result.Welcome.LLMCooldownSeconds != 60 {
		t.Fatalf("welcome fields not saved/applied: %+v", result.Welcome)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "welcome_mode": "llm", "welcome_llm_cooldown_seconds": 0}); err != nil {
		t.Fatal(err)
	}
	actual := r.effectiveConfigForEvent(event)
	if actual.WelcomeMode != WelcomeModeLLM || actual.WelcomeLLMCooldownSeconds != 30 || actual.WelcomeMessage != "回落欢迎" || len(actual.WelcomeTemplates) != 2 {
		t.Fatalf("partial update did not preserve fields or return to inherited cooldown: %+v", welcomeSettingsFromConfig(actual))
	}
}

func TestBotConfigWelcomeVerifiesGroupAdministrator(t *testing.T) {
	for _, role := range []string{"admin", "member"} {
		t.Run(role, func(t *testing.T) {
			channel := &recordingChannel{apiResponses: map[string]map[string]any{"get_group_member_info": {"user_id": "member", "role": role}}}
			r := NewRuntime(BotConfig{OwnerID: "owner"}, channel, NewPluginManager(), nil, nil, nil, nil)
			r.SetGroupConfigStore(&testWritableGroupConfigStore{})
			event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "member", SenderRole: "group_admin"}
			_, err := newDianaBotConfigTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "welcome_enabled": true})
			if (err == nil) != (role == "admin") {
				t.Fatalf("live role %s: %v", role, err)
			}
		})
	}
}

func TestBotConfigDiagnosticsRequireOwnerOfEventBot(t *testing.T) {
	a := BotConfig{ID: "a", OwnerID: "a-owner", OneBotAccessToken: "a-secret"}
	b := BotConfig{ID: "b", OwnerID: "b-owner", OneBotAccessToken: "b-secret"}
	r := NewRuntime(a, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{a, b}})
	for _, user := range []string{"member", "a-owner"} {
		tool := newDianaBotConfigTool(r, MessageEvent{ProfileID: "b", Kind: EventKindGroup, GroupID: "123", UserID: user, SenderRole: "group_admin"})
		for _, section := range []string{"all", "bot", "llm", "skills", "runtime", "paths"} {
			if _, err := tool.Run(context.Background(), map[string]any{"operation": "get", "section": section}); err == nil {
				t.Fatalf("%s read owner diagnostics %s", user, section)
			}
		}
	}
	tool := newDianaBotConfigTool(r, MessageEvent{ProfileID: "b", UserID: "b-owner"})
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "get", "section": "bot", "scope": "bot"})
	if err != nil || strings.Contains(raw, "b-secret") || strings.Contains(raw, "a-owner") || !strings.Contains(raw, "b-owner") {
		t.Fatalf("owner snapshot scope/redaction: %s, %v", raw, err)
	}
	// Real model replay supplies the bot scope explicitly for full diagnostics.
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "get", "section": "all", "scope": "bot"}); err != nil {
		t.Fatalf("explicit bot scope diagnostics rejected: %v", err)
	}
	for _, input := range []map[string]any{
		{"operation": "get", "section": "unknown"},
		{"operation": "update", "section": "all", "welcome_enabled": true},
		{"operation": "get", "section": "bot", "profile_id": "a"},
		{"operation": "get", "section": "all", "scope": "group"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("invalid diagnostic request accepted: %v", input)
		}
	}
	adminTool := newDianaBotConfigTool(r, MessageEvent{ProfileID: "b"})
	adminTool.admin = true
	if _, err := adminTool.Run(context.Background(), map[string]any{"operation": "get", "section": "runtime"}); err != nil {
		t.Fatalf("authenticated WebUI diagnostics denied: %v", err)
	}
}

func TestBotConfigIsTheOnlyRegisteredConfigurationTool(t *testing.T) {
	cfg := standardModeBotConfig()
	cfg.OwnerID = "owner"
	r := NewRuntime(cfg, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{UserID: "owner", Kind: EventKindPrivate}
	registry, err := r.newReplyAgentRegistry(context.Background(), cfg.WithDefaults(), event, RelationshipPolicy{Owner: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, exists := registry.Get("config"); exists {
		t.Fatal("removed config tool remains registered")
	}
	if _, exists := registry.Get(botConfigToolName); !exists {
		t.Fatal("unified configuration tool missing")
	}
}
