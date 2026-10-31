// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 思考强度按路由保存，主路由和后备路由都归一化。
func TestModelRoleReasoningEffortNormalize(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"intent": {ProfileID: "p", Model: "m", ReasoningEffort: " None ", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m2", ReasoningEffort: "high"}}},
		"chat":   {ProfileID: "p", Model: "m", ReasoningEffort: "bogus"},
	})
	if got := roles["intent"].ReasoningEffort; got != "none" {
		t.Fatalf("intent effort = %q, want none", got)
	}
	if got := roles["intent"].Fallbacks[0].ReasoningEffort; got != "high" {
		t.Fatalf("fallback effort = %q, want high", got)
	}
	if got := roles["chat"].ReasoningEffort; got != "" {
		t.Fatalf("invalid effort kept: %q", got)
	}
	cfg := ConfigFromPayload(PayloadFromConfig(BotConfig{ModelRoles: roles}), BotConfig{})
	if got := cfg.ModelRoles["intent"].ReasoningEffort; got != "none" {
		t.Fatalf("round trip effort = %q", got)
	}
	if got := cfg.ModelRoles["intent"].Fallbacks[0].ReasoningEffort; got != "high" {
		t.Fatalf("fallback round trip effort = %q", got)
	}
}

// 主路由和后备路由使用各自的思考强度；没设就不发，不看提供商上的旧值。
// 没单独绑的细分用途继承分组那一档的设置。
func TestRoleBoundProfilesApplyReasoningEffort(t *testing.T) {
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "main", Config: llm.ProviderConfig{Model: "m", ReasoningEffort: "high"}},
		{ID: "backup", Config: llm.ProviderConfig{Model: "m2", ReasoningEffort: "high"}},
	}}
	r := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "main", Model: "m"},
		"intent": {ProfileID: "main", Model: "m", ReasoningEffort: "none", Fallbacks: []ModelRole{{ProfileID: "backup", Model: "m2", ReasoningEffort: "low"}}},
	}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	profiles, err := r.roleBoundProfiles(PurposeProactiveReplyRouter, set, llm.GroupIntent)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles = %+v", profiles)
	}
	for i, profile := range profiles {
		want := []string{"none", "low"}[i]
		if profile.Config.ReasoningEffort != want {
			t.Fatalf("%s effort = %q, want %q", profile.ID, profile.Config.ReasoningEffort, want)
		}
	}
	unset, err := r.roleBoundProfiles(PurposeProactiveReplyRouter, set, llm.GroupIntent, map[string]ModelRole{
		"intent": {ProfileID: "main", Model: "m", ReasoningEffort: "high", Fallbacks: []ModelRole{{ProfileID: "backup", Model: "m2"}}},
	})
	if err != nil || len(unset) != 2 || unset[1].Config.ReasoningEffort != "" {
		t.Fatalf("unset fallback inherited effort: profiles=%+v err=%v", unset, err)
	}
	chat, err := r.roleBoundProfiles(PurposeReply, set, llm.GroupChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat) != 1 || chat[0].Config.ReasoningEffort != "" {
		t.Fatalf("unset role must not send provider effort: %+v", chat)
	}
	if set.Profiles[0].Config.ReasoningEffort != "high" {
		t.Fatal("override leaked into the stored profile set")
	}
}

func TestCandidateReasoningRequest(t *testing.T) {
	profile := llm.Profile{Config: llm.ProviderConfig{ReasoningEffort: "none"}}
	if got := candidateReasoningRequest(llm.GenerateRequest{}, profile).ReasoningEffort; got != "none" {
		t.Fatalf("effort = %q, want none", got)
	}
	if got := candidateReasoningRequest(llm.GenerateRequest{ReasoningEffort: "low"}, profile).ReasoningEffort; got != "low" {
		t.Fatalf("caller effort overridden: %q", got)
	}
}
