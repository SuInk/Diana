// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 思考强度按用途覆盖：存盘时归一化、认不出的档位丢掉，后备路由不单独带。
func TestModelRoleReasoningEffortNormalize(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"intent": {ProfileID: "p", Model: "m", ReasoningEffort: " None ", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m2", ReasoningEffort: "high"}}},
		"chat":   {ProfileID: "p", Model: "m", ReasoningEffort: "bogus"},
	})
	if got := roles["intent"].ReasoningEffort; got != "none" {
		t.Fatalf("intent effort = %q, want none", got)
	}
	if got := roles["intent"].Fallbacks[0].ReasoningEffort; got != "" {
		t.Fatalf("fallback effort = %q, want empty", got)
	}
	if got := roles["chat"].ReasoningEffort; got != "" {
		t.Fatalf("invalid effort kept: %q", got)
	}
	cfg := ConfigFromPayload(PayloadFromConfig(BotConfig{ModelRoles: roles}), BotConfig{})
	if got := cfg.ModelRoles["intent"].ReasoningEffort; got != "none" {
		t.Fatalf("round trip effort = %q", got)
	}
}

// 用途上设了思考强度，主路由和后备路由的候选都换成它；没设就沿用提供商配置。
// 没单独绑的细分用途继承分组那一档的设置。
func TestRoleBoundProfilesApplyReasoningEffort(t *testing.T) {
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "main", Config: llm.ProviderConfig{Model: "m", ReasoningEffort: "high"}},
		{ID: "backup", Config: llm.ProviderConfig{Model: "m2", ReasoningEffort: "high"}},
	}}
	r := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "main", Model: "m"},
		"intent": {ProfileID: "main", Model: "m", ReasoningEffort: "none", Fallbacks: []ModelRole{{ProfileID: "backup", Model: "m2"}}},
	}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	profiles, err := r.roleBoundProfiles(PurposeProactiveReplyRouter, set, llm.GroupIntent)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles = %+v", profiles)
	}
	for _, profile := range profiles {
		if profile.Config.ReasoningEffort != "none" {
			t.Fatalf("%s effort = %q, want none", profile.ID, profile.Config.ReasoningEffort)
		}
	}
	chat, err := r.roleBoundProfiles(PurposeReply, set, llm.GroupChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat) != 1 || chat[0].Config.ReasoningEffort != "high" {
		t.Fatalf("chat should keep provider effort: %+v", chat)
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
