package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBotConfigOffOverridesInheritedParticipation(t *testing.T) {
	r80, s90 := 80, 90
	base := BotConfig{ID: "a", OwnerID: "owner", Participation: &ParticipationPreferences{Desire: 75, CooldownSeconds: 120, RelevanceThreshold: &r80, SubstanceThreshold: &s90}, ProactiveReplyThreshold: .93}
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{ActiveID: "a", Profiles: []BotConfig{base, {ID: "b", OwnerID: "other", Participation: &ParticipationPreferences{Desire: 100, CooldownSeconds: 30}}}})
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "owner"}
	tool := newDianaBotParticipationTool(r, event)
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "update", "desire_level": "off"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Participation ParticipationPreferences `json:"participation"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if response.Participation.Desire != 0 {
		t.Fatal("returned success without disabling participation")
	}
	actual := r.effectiveConfigForEvent(event).participationPreferences()
	if actual.Desire != 0 || actual.CooldownSeconds != 120 || actual.relevanceThreshold() != 80 || actual.substanceThreshold() != 90 {
		t.Fatalf("effective preferences wrong: %+v", actual)
	}
	if r.Config().Participation.Desire != 75 || r.effectiveConfigForEvent(MessageEvent{ProfileID: "b"}).Participation.Desire != 100 {
		t.Fatal("group update changed robot defaults")
	}
	if !(proactiveReplyDecision{ShouldReply: true, Category: "bot_related", DirectedAtBot: true}).allows(0, r.effectiveConfigForEvent(event).chatInSettings()) {
		t.Fatal("off blocked explicit requests")
	}
	data, _ := json.Marshal(store.set)
	var restored GroupConfigSet
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	store.set = restored
	if r.effectiveConfigForEvent(event).participationPreferences().Desire != 0 {
		t.Fatal("restart lost off")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "desire_level": "low", "substance_level": "high", "cooldown_seconds": 0}); err != nil {
		t.Fatal(err)
	}
	actual = r.effectiveConfigForEvent(event).participationPreferences()
	if actual.Desire != 25 || actual.CooldownSeconds != 0 || actual.relevanceThreshold() != 80 || actual.substanceThreshold() != 80 {
		t.Fatalf("partial update lost values: %+v", actual)
	}
}

type failedGroupPolicyStore struct{ testWritableGroupConfigStore }

func (*failedGroupPolicyStore) SaveGroupConfig(GroupConfig, BotConfig) (GroupConfig, error) {
	return GroupConfig{}, errors.New("disk full")
}

func TestBotConfigRejectsInvalidFieldsAndPersistenceFailure(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &failedGroupPolicyStore{}
	r.SetGroupConfigStore(store)
	tool := newDianaBotParticipationTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "owner"})
	for _, input := range []map[string]any{
		{"operation": "update", "desire_level": "invalid"},
		{"operation": "update", "cooldown_seconds": -1},
		{"operation": "update", "cooldown_seconds": 1.5},
		{"operation": "update", "chat_in_enabled": false},
		{"operation": "update", "scope": "other", "desire_level": "off"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("invalid update accepted: %v", input)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "desire_level": "off"}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatal(err)
	}
	if len(store.set.Groups) != 0 {
		t.Fatal("failed write mutated group")
	}
}

func TestGroupToolsAndSkillsDoNotExposeOldMixedTool(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "member"}
	registry, err := r.newAgentRegistry(context.Background(), r.Config(), event, RelationshipPolicyFor(UserMemoryProfile{}, "owner", "member"), newDianaGroupTool(r, event), newDianaBotParticipationTool(r, event))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get("diana.onebot_group"); ok {
		t.Fatal("old mixed tool remains registered")
	}
	if _, ok := registry.Get(botParticipationToolName); !ok {
		t.Fatal("config tool unavailable to group admins")
	}
	group := newDianaGroupTool(r, event)
	for _, op := range []string{"set_reply_policy", "members", "info"} {
		if _, err := group.Run(context.Background(), map[string]any{"operation": op}); err == nil {
			t.Fatal("OneBot bypassed protocol/config separation")
		}
	}
	skills := r.botProtocolBuiltinSkills(event)
	if len(skills) != 2 || !strings.Contains(skills[1].Content, "desire_level") {
		t.Fatal("protocol skill missing")
	}
}

type participationToolSaver struct {
	cfg  BotConfig
	fail bool
}

func (*participationToolSaver) SaveBotConfig(BotConfig) { panic("legacy config save used") }
func (s *participationToolSaver) SaveParticipation(expected BotConfig, prefs ParticipationPreferences) (BotConfig, error) {
	if s.fail {
		return BotConfig{}, errors.New("disk full")
	}
	if expected.ID != s.cfg.ID {
		return BotConfig{}, errors.New("wrong bot")
	}
	s.cfg.Participation = copyParticipation(&prefs)
	return s.cfg, nil
}

func TestBotConfigOwnerScopeAndFailedSave(t *testing.T) {
	a := BotConfig{ID: "a", OwnerID: "a-owner", Participation: &ParticipationPreferences{Desire: 75, CooldownSeconds: 120}}
	b := BotConfig{ID: "b", OwnerID: "b-owner", Participation: &ParticipationPreferences{Desire: 50, CooldownSeconds: 90}}
	saver := &participationToolSaver{cfg: b}
	r := NewRuntime(a, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
	r.SetProfiles(ProfileSet{ActiveID: "a", Profiles: []BotConfig{a, b}})
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "b", UserID: "b-owner"}
	tool := newDianaBotParticipationTool(r, event)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "desire_level": "off"}); err != nil {
		t.Fatal(err)
	}
	if r.effectiveConfigForEvent(event).Participation.Desire != 0 || r.Config().Participation.Desire != 75 || saver.cfg.Participation.CooldownSeconds != 90 {
		t.Fatal("bot-scoped configuration update was not isolated")
	}
	saver.fail = true
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "desire_level": "max"}); err == nil {
		t.Fatal("failed save reported success")
	}
	if r.effectiveConfigForEvent(event).Participation.Desire != 0 {
		t.Fatal("failed save changed runtime")
	}
	for _, user := range []string{"member", "a-owner"} {
		event.UserID = user
		if _, err := newDianaBotParticipationTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "scope": "bot", "desire_level": "max"}); err == nil {
			t.Fatal("non-owner changed another bot")
		}
	}
}

func TestBotConfigDoesNotTrustOldAdministratorRole(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{"get_group_member_info": {"user_id": "member", "role": "member"}}}
	r := NewRuntime(BotConfig{OwnerID: "owner"}, channel, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&testWritableGroupConfigStore{})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "member", SenderRole: "group_admin"}
	if _, err := newDianaBotParticipationTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "desire_level": "off"}); err == nil {
		t.Fatal("stale administrator role allowed mutation")
	}
}

func TestChangingMemberLevelPreservesParticipationInheritance(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner", Participation: &ParticipationPreferences{Desire: 75}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "owner"}
	if _, err := newDianaBotParticipationTool(r, event).Run(context.Background(), map[string]any{"operation": "update", "minimum_reply_member_level": 10}); err != nil {
		t.Fatal(err)
	}
	saved, ok := store.ConfigForGroup("", "g")
	if !ok || saved.Participation != nil || saved.ResponseMode != "" {
		t.Fatal("member level update froze inherited participation")
	}
}
