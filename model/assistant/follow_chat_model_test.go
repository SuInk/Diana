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

// 「跟随对话」不再是视觉理解的特权：意图识别、图片生成这些用途选了跟随，也要解析
// 到对话那一档的绑定，而不是被 normalizeModelRoles 悄悄抹掉。
func TestFollowChatAppliesToEveryNonChatRole(t *testing.T) {
	chat := ModelRole{ProfileID: "chat-provider", Model: "chat-model"}
	for _, tc := range []struct {
		key   string
		group string
	}{
		{"vision", llm.GroupVision},
		{"intent", llm.GroupIntent},
		{"image", llm.GroupImage},
		{"embedding", llm.GroupEmbedding},
	} {
		roles := normalizeModelRoles(map[string]ModelRole{
			"chat": chat,
			tc.key: {FollowChat: true},
		})
		if got, ok := roles[tc.key]; !ok || !got.FollowChat {
			t.Fatalf("%s 的跟随对话被丢掉了：%+v", tc.key, roles)
		}
		got, ok := modelRoleFor(roles, "", tc.group)
		if !ok || !reflect.DeepEqual(got, roles["chat"]) {
			t.Fatalf("%s 没有跟随对话：%+v", tc.key, got)
		}
	}
}

// 单个用途（不是分组）也能选跟随对话：记忆抽取跟随时，解析结果就是对话的绑定，
// 而不是它所属的意图分组。
func TestFollowChatOnPurposeResolvesToChat(t *testing.T) {
	chat := ModelRole{ProfileID: "chat-provider", Model: "chat-model"}
	roles := normalizeModelRoles(map[string]ModelRole{
		"chat":               chat,
		"intent":             {ProfileID: "intent-provider", Model: "intent-model"},
		PurposeMemoryExtract: {FollowChat: true},
	})
	got, ok := modelRoleFor(roles, PurposeMemoryExtract, llm.GroupIntent)
	if !ok || !reflect.DeepEqual(got, roles["chat"]) {
		t.Fatalf("用途级跟随对话没有解析到对话绑定：%+v", got)
	}
	// 没跟随的用途仍然走它自己的分组绑定，不受影响。
	got, ok = modelRoleFor(roles, PurposeMemorySummary, llm.GroupIntent)
	if !ok || got.Model != "intent-model" {
		t.Fatalf("同分组的其他用途被带偏了：%+v", got)
	}
}
