package assistant

import (
	"github.com/SuInk/diana/model/llm"
	"reflect"
	"testing"
)

func TestVisionFollowChatRoundTripAndDynamicResolution(t *testing.T) {
	cfg := BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "chat-provider", Model: "chat-model", Fallbacks: []ModelRole{{ProfileID: "backup", Model: "backup-model"}}},
		"vision": {FollowChat: true, ProfileID: "old-vision", Model: "old-model", Fallbacks: []ModelRole{{ProfileID: "old-backup", Model: "old"}}},
	}}
	cfg = ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{})
	vision := cfg.ModelRoles["vision"]
	if !vision.FollowChat || vision.ProfileID != "" || vision.Model != "" || len(vision.Fallbacks) != 0 {
		t.Fatalf("stale vision binding: %+v", vision)
	}
	for _, purpose := range []string{"", PurposeReply, "image_description_cache"} {
		got, ok := modelRoleFor(cfg.ModelRoles, purpose, llm.GroupVision)
		if !ok || !reflect.DeepEqual(got, cfg.ModelRoles["chat"]) {
			t.Fatalf("vision did not follow chat: %+v", got)
		}
	}
	cfg.ModelRoles["chat"] = ModelRole{ProfileID: "new-provider", Model: "new-model"}
	got, _ := modelRoleFor(cfg.ModelRoles, PurposeReply, llm.GroupVision)
	if got.Model != "new-model" || got.ProfileID != "new-provider" {
		t.Fatalf("follow was a copy: %+v", got)
	}
	cfg.ModelRoles["vision"] = ModelRole{ProfileID: "vision-provider", Model: "vision-model"}
	got, _ = modelRoleFor(cfg.ModelRoles, PurposeReply, llm.GroupVision)
	if got.Model != "vision-model" {
		t.Fatal("explicit vision model ignored")
	}
}

func TestVisionFollowChatWithoutChatFailsClosed(t *testing.T) {
	role, ok := modelRoleFor(map[string]ModelRole{"vision": {FollowChat: true}}, "", llm.GroupVision)
	if !ok {
		t.Fatal("missing chat must not select default vision group")
	}
	if _, err := profilesForModelRole(llm.ProfileSet{}, role); err == nil {
		t.Fatal("missing chat accepted")
	}
	roles := normalizeModelRoles(map[string]ModelRole{"chat": {FollowChat: true}})
	if _, ok := roles["chat"]; ok {
		t.Fatal("chat cannot follow itself")
	}
}

func TestVisionFollowChatUsesOnlyChatFallbackChain(t *testing.T) {
	r := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "chat", Model: "main", Fallbacks: []ModelRole{{ProfileID: "backup", Model: "second"}}},
		"vision": {FollowChat: true},
	}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "vision-only", Group: llm.GroupVision, Config: llm.ProviderConfig{Model: "visual"}},
		{ID: "chat", Config: llm.ProviderConfig{Model: "main"}},
		{ID: "backup", Config: llm.ProviderConfig{Model: "second"}},
	}}
	profiles, err := r.roleBoundProfiles(PurposeReply, set, llm.GroupVision)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ID != "chat" || profiles[1].ID != "backup" {
		t.Fatalf("unexpected vision route: %+v", profiles)
	}
}
