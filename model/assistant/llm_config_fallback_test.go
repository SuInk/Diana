// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func testProfileInGroup(group string) llm.Profile {
	return llm.Profile{ID: "picked", Name: "Picked", Group: group}
}

func modelSwitchRuntimeWithFallbacks(t *testing.T, chat ModelRole) (*Runtime, *scopedRoleTestSaver, MessageEvent) {
	t.Helper()
	r, saver, _, event := modelSwitchTestRuntime(t)
	bot := saver.configs["b"]
	roles := map[string]ModelRole{}
	for key, role := range bot.ModelRoles {
		roles[key] = role
	}
	roles["chat"] = chat
	bot.ModelRoles = roles
	saver.configs["b"] = bot
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{saver.configs["a"], bot}})
	return r, saver, event
}

// New selections preserve both the previous primary and existing fallbacks.
func TestChatModelSwitchKeepsFallbackRoutes(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"没点名供应商": {"model": "other"},
		"点名当前这家": {"provider_id": "one", "model": "other"},
	} {
		t.Run(name, func(t *testing.T) {
			fallbacks := []ModelRole{{ProfileID: "two", Model: "shared"}, {ProfileID: "one", Model: "shared"}}
			r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{ProfileID: "one", Model: "shared", Fallbacks: fallbacks})
			reply, err := newDianaLLMConfigTool(r, event).Run(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			saved := saver.configs["b"].ModelRoles["chat"]
			if saved.ProfileID != "one" || saved.Model != "other" {
				t.Fatalf("primary route not switched: %+v", saved)
			}
			if len(saved.Fallbacks) != 3 || saved.Fallbacks[0].ProfileID != "one" || saved.Fallbacks[0].Model != "shared" || saved.Fallbacks[1].ProfileID != "two" || saved.Fallbacks[2].ProfileID != "one" {
				t.Fatalf("fallback routes lost or reordered: %+v", saved.Fallbacks)
			}
			if strings.Contains(reply, "后备路由已被") {
				t.Fatalf("reply claims a replacement that did not happen: %s", reply)
			}
			// 运行态也要拿到同一份，否则下一次调用还按旧的走。
			if live := r.effectiveConfigForEvent(event).ModelRoles["chat"]; len(live.Fallbacks) != 3 {
				t.Fatalf("runtime lost the fallback routes: %+v", live)
			}
		})
	}
}

func TestCrossProviderSwitchPreservesModels(t *testing.T) {
	fallbacks := []ModelRole{{ProfileID: "two", Model: "other", Weight: 3, ReasoningEffort: "low"}, {ProfileID: "two", Model: "shared", Standby: true}}
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{ProfileID: "one", Model: "shared", Fallbacks: fallbacks, RoutingStrategy: "weighted", ReasoningEffort: "high", Weight: 2})
	reply, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"})
	if err != nil {
		t.Fatal(err)
	}
	saved := saver.configs["b"].ModelRoles["chat"]
	if saved.ProfileID != "two" || saved.Model != "other" || saved.Weight != 3 || saved.RoutingStrategy != "weighted" || saved.ReasoningEffort != "low" {
		t.Fatalf("lost selected route or policy: %+v", saved)
	}
	if len(saved.Fallbacks) != 2 || saved.Fallbacks[0].ProfileID != "one" || saved.Fallbacks[0].Weight != 2 || saved.Fallbacks[0].ReasoningEffort != "high" || saved.Fallbacks[1].ProfileID != "two" || saved.Fallbacks[1].Model != "shared" || !saved.Fallbacks[1].Standby {
		t.Fatalf("lost routes: %+v", saved.Fallbacks)
	}
	if strings.Contains(reply, "已被这次绑定替换") {
		t.Fatal(reply)
	}
	// Selecting the primary again must not append another copy.
	if _, err = newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"}); err != nil {
		t.Fatal(err)
	}
	if got := saver.configs["b"].ModelRoles["chat"]; !reflect.DeepEqual(saved, got) {
		t.Fatalf("repeat changed order: %+v", got)
	}
}

func TestGroupBoundRolePreservesPreviousRoutes(t *testing.T) {
	original := ModelRole{Group: "chat", Model: "shared", Fallbacks: []ModelRole{{ProfileID: "two", Model: "other"}}}
	for _, explicit := range []bool{false, true} {
		got, replaced := nextModelRole(original, testProfileInGroup("chat"), "other", explicit)
		if replaced || len(got.Fallbacks) != 2 || got.Fallbacks[0].Group != "chat" || got.Fallbacks[0].Model != "shared" || got.Fallbacks[1].ProfileID != "two" || got.Fallbacks[1].Model != "other" {
			t.Fatalf("lost group binding: %+v", got)
		}
		if explicit && got.ProfileID != "picked" {
			t.Fatal("explicit provider not selected")
		}
		if !explicit && got.Group != "chat" {
			t.Fatal("implicit group selection lost")
		}
	}
}

func TestPrioritizeRegistryRoutePreservesAttributes(t *testing.T) {
	selected := ModelRole{ProviderID: "two", ModelID: "two:other", Model: "other", Weight: 5, Disabled: true, Standby: true}
	original := ModelRole{ProfileID: "one", Model: "shared", RoutingStrategy: "round_robin", Fallbacks: []ModelRole{selected, {ProfileID: "three", Model: "last"}}}
	got := prioritizeModelRole(original, ModelRole{ProfileID: "two", Model: "other"})
	if got.ProviderID != "two" || got.ModelID != "two:other" || got.Weight != 5 || !got.Disabled || !got.Standby || got.RoutingStrategy != "round_robin" || len(got.Fallbacks) != 2 {
		t.Fatalf("lost route attributes: %+v", got)
	}
	if original.Fallbacks[0].ProviderID != "two" || original.ProfileID != "one" {
		t.Fatal("mutated source")
	}
}

func TestGroupPreferenceRetainsBotRoutesAndUpdates(t *testing.T) {
	base := ModelRole{ProfileID: "one", Model: "shared", Fallbacks: []ModelRole{{ProfileID: "two", Model: "other"}, {ProfileID: "one", Model: "other"}}, RoutingStrategy: "failover"}
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, base)
	event.Kind = EventKindGroup
	event.GroupID = "g"
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	if err := r.saveGroupChatModel(event, GroupChatModel{ProfileID: "two", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	got := r.effectiveConfigForEvent(event).ModelRoles["chat"]
	if got.ProfileID != "two" || len(got.Fallbacks) != 2 || got.Fallbacks[0].ProfileID != "one" || got.Fallbacks[0].Model != "shared" || got.Fallbacks[1].Model != "other" || got.RoutingStrategy != "failover" {
		t.Fatalf("group lost routes: %+v", got)
	}
	bot := saver.configs["b"]
	bot.ModelRoles["chat"] = ModelRole{ProfileID: "one", Model: "shared", Fallbacks: append(base.Fallbacks, ModelRole{ProfileID: "three", Model: "new"})}
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{saver.configs["a"], bot}})
	got = r.effectiveConfigForEvent(event).ModelRoles["chat"]
	if len(got.Fallbacks) != 3 || got.Fallbacks[2].ProfileID != "three" {
		t.Fatalf("group did not follow new fallback: %+v", got)
	}
	if r.ProfileConfig("b").ModelRoles["chat"].ProfileID != "one" {
		t.Fatal("changed global order")
	}
	if err := r.saveGroupChatModel(event, GroupChatModel{}); err != nil {
		t.Fatal(err)
	}
	if got = r.effectiveConfigForEvent(event).ModelRoles["chat"]; got.ProfileID != "one" || len(got.Fallbacks) != 3 {
		t.Fatal("reset lost bot routes")
	}
}
