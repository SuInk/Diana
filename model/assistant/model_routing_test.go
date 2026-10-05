// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func routingTestRuntime(t *testing.T, role ModelRole) (*Runtime, map[string]*retryRegistryAdapter, llm.ProfileSet) {
	t.Helper()
	registry := llm.NewProviderRegistry()
	adapters := map[string]*retryRegistryAdapter{}
	set := llm.ProfileSet{}
	for _, id := range []string{"a", "b", "c", "d"} {
		adapter := &retryRegistryAdapter{succeedAt: 1, response: id}
		adapters[id] = adapter
		if err := registry.RegisterProvider(llm.ProviderDefinition{ID: id, Name: id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, adapter); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterModel(llm.ModelDefinition{ID: id + ":m", ProviderID: id, ModelID: "m", Name: "m"}); err != nil {
			t.Fatal(err)
		}
		set.Profiles = append(set.Profiles, llm.Profile{ID: id, Group: "pool", Config: llm.ProviderConfig{Model: "m", BaseURL: "https://" + id + ".invalid"}})
	}
	r := NewRuntime(BotConfig{ID: "bot", ModelRoles: map[string]ModelRole{"chat": role}}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: set}, nil, nil, nil)
	r.SetLLMProviderRegistry(registry)
	return r, adapters, set
}

func TestModelRoutingActualProviderRuns(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		want     []string
	}{
		{"", []string{"a", "a", "a", "a"}},
		{"round_robin", []string{"a", "b", "a", "b"}},
		{"weighted", []string{"a", "a", "b", "a", "a", "a", "b", "a"}},
	} {
		t.Run("strategy="+tc.strategy, func(t *testing.T) {
			r, adapters, _ := routingTestRuntime(t, ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: tc.strategy, Weight: 3, Fallbacks: []ModelRole{
				{ProfileID: "b", Model: "m", Weight: 1}, {ProfileID: "c", Model: "m", Standby: true}, {ProfileID: "d", Model: "m", Disabled: true},
			}})
			for _, want := range tc.want {
				got, err := r.runRawLLMProviderForGroup(context.Background(), llm.GroupChat, func(provider LLMProvider) (string, error) {
					// Tool-call follow-ups in a provider run must retain its channel.
					first, err := provider.Generate(context.Background(), llm.GenerateRequest{})
					if err != nil {
						return "", err
					}
					second, err := provider.Generate(context.Background(), llm.GenerateRequest{})
					if err != nil {
						return "", err
					}
					return first.Text + second.Text, nil
				})
				if err != nil || got != want+want {
					t.Fatalf("got=%q err=%v, want=%q", got, err, want+want)
				}
			}
			if adapters["c"].calls != 0 || adapters["d"].calls != 0 {
				t.Fatal("standby or disabled route participated in normal balancing")
			}
		})
	}
}

func TestModelRoutingFailoverAndDisabledRoutes(t *testing.T) {
	r, adapters, _ := routingTestRuntime(t, ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "round_robin", Fallbacks: []ModelRole{
		{ProfileID: "d", Model: "m", Disabled: true}, {ProfileID: "c", Model: "m", Standby: true}, {ProfileID: "b", Model: "m"},
	}})
	adapters["a"].err = errors.New("503 service unavailable")
	adapters["b"].err = errors.New("503 service unavailable")
	for range 2 {
		got, err := r.runRawLLMProviderForGroup(context.Background(), llm.GroupChat, func(provider LLMProvider) (string, error) {
			response, err := provider.Generate(context.Background(), llm.GenerateRequest{})
			if err != nil {
				return "", err
			}
			return response.Text, nil
		})
		if err != nil || got != "c" {
			t.Fatalf("got=%q err=%v, want standby", got, err)
		}
	}
	if adapters["a"].calls != 2 || adapters["b"].calls != 2 || adapters["c"].calls != 2 || adapters["d"].calls != 0 {
		t.Fatalf("unexpected attempts: a=%d b=%d c=%d d=%d", adapters["a"].calls, adapters["b"].calls, adapters["c"].calls, adapters["d"].calls)
	}
	cfg := r.profileConfig("")
	cfg.ModelRoles["chat"] = ModelRole{ProfileID: "a", Model: "m", Disabled: true}
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	_, err := r.runRawLLMProviderForGroup(context.Background(), llm.GroupChat, func(LLMProvider) (string, error) {
		t.Fatal("all-disabled role fell back to a global provider")
		return "", nil
	})
	if err == nil {
		t.Fatal("all-disabled role was accepted")
	}
}

func TestModelRoutingScopeCatalogAndConfigChanges(t *testing.T) {
	role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "round_robin", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m"}}}
	r, _, set := routingTestRuntime(t, role)
	roles := map[string]ModelRole{"chat": role}
	check := func(bot, purpose, want string) {
		t.Helper()
		ctx := context.WithValue(context.Background(), modelProfileContextKey{}, bot)
		profiles, err := r.scheduledRoleProfiles(ctx, purpose, set, llm.GroupChat, roles)
		if err != nil || profiles[0].ID != want {
			t.Fatalf("bot=%s purpose=%s profiles=%v err=%v, want=%s", bot, purpose, profiles, err, want)
		}
	}
	check("one", PurposeReply, "a")
	for range 3 {
		if _, err := r.roleBoundProfiles(PurposeReply, set, llm.GroupChat, roles); err != nil {
			t.Fatal(err)
		}
	}
	check("one", PurposeReply, "b")
	check("two", PurposeReply, "a")
	check("one", PurposeSubagent, "a")
	role.Fallbacks = append(role.Fallbacks, ModelRole{ProfileID: "c", Model: "m"})
	roles["chat"] = role
	check("one", PurposeReply, "a")
	check("one", PurposeReply, "b")
	check("one", PurposeReply, "c")
}

func TestModelRoutingGroupsAndConcurrentWeights(t *testing.T) {
	r, _, set := routingTestRuntime(t, ModelRole{ProfileID: "a", Model: "m"})
	roles := map[string]ModelRole{"chat": {Group: "pool", Model: "m", RoutingStrategy: "round_robin"}}
	for _, want := range []string{"a", "b", "c", "d", "a"} {
		profiles, err := r.scheduledRoleProfiles(context.Background(), PurposeReply, set, llm.GroupChat, roles)
		if err != nil || profiles[0].ID != want {
			t.Fatalf("group profiles=%v err=%v, want=%s", profiles, err, want)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := [2]int{}
	for range 200 {
		wg.Go(func() {
			selected := r.nextModelRoute("parallel", "same", "weighted", []int{3, 1})
			mu.Lock()
			counts[selected]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if counts != [2]int{150, 50} {
		t.Fatalf("weighted concurrent counts=%v", counts)
	}
}

func TestModelRoutingImageInspectionDoesNotConsumeSlots(t *testing.T) {
	r, _, _ := routingTestRuntime(t, ModelRole{ProfileID: "a", Model: "m"})
	cfg := r.profileConfig("")
	cfg.ModelRoles["image"] = ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "round_robin", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m"}, {ProfileID: "c", Model: "m", Standby: true}}}
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	for _, want := range []string{"https://a.invalid", "https://b.invalid"} {
		_ = r.imageProviderConfigs()
		_ = r.imageProviderConfigs()
		configs := r.resolveImageProviderConfigs(context.Background(), true)
		if len(configs) != 3 || configs[0].BaseURL != want || configs[0].ImageModel != "m" || configs[2].BaseURL != "https://c.invalid" {
			t.Fatalf("image configs=%v, want first=%s and standby last", configs, want)
		}
	}
}

func TestModelRoutingLegacyAndBackgroundMemoryRuns(t *testing.T) {
	role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "round_robin", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m"}}}
	r, _, _ := routingTestRuntime(t, role)
	r.SetLLMProviderRegistry(nil)
	r.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		return &capturingLLMProvider{reply: cfg.BaseURL}, nil
	})
	run := func(provider LLMProvider) (string, error) {
		response, err := provider.Generate(context.Background(), llm.GenerateRequest{})
		if err != nil {
			return "", err
		}
		return response.Text, nil
	}
	for _, want := range []string{"https://a.invalid", "https://b.invalid"} {
		got, err := r.runRawLLMProviderForGroup(context.Background(), llm.GroupChat, run)
		if err != nil || got != want {
			t.Fatalf("legacy run=%s err=%v, want=%s", got, err, want)
		}
	}
	cfg := r.profileConfig("")
	cfg.ModelRoles["background"] = role
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	r.llmStore.(*stubLLMProfileStore).set.Profiles[3].Group = "memory"
	ctx := withLLMUsagePurpose(context.Background(), PurposeMemoryExtract)
	for _, want := range []string{"https://a.invalid", "https://b.invalid"} {
		got, err := r.runLLMMemoryProvider(ctx, run)
		if err != nil || got != want {
			t.Fatalf("background memory run=%s err=%v, want=%s", got, err, want)
		}
	}
}

func TestModelRoutingConfigRoundTripAndNormalization(t *testing.T) {
	role := normalizeModelRole(ModelRole{ProfileID: " a ", Model: " m ", RoutingStrategy: " weighted ", Weight: 2000, Disabled: true, Fallbacks: []ModelRole{{ProfileID: "b", Model: "m", Weight: 2, Standby: true, RoutingStrategy: "round_robin"}}})
	if role.Weight != 1000 || role.RoutingStrategy != "weighted" || role.Fallbacks[0].RoutingStrategy != "" {
		t.Fatalf("normalized role=%+v", role)
	}
	data, err := json.Marshal(role)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ModelRole
	if err := json.Unmarshal(data, &roundTrip); err != nil || !reflect.DeepEqual(role, normalizeModelRole(roundTrip)) {
		t.Fatalf("round trip=%+v err=%v", roundTrip, err)
	}
	if got := normalizeModelRole(ModelRole{FollowChat: true, RoutingStrategy: "weighted", Weight: 4, Disabled: true}); !reflect.DeepEqual(got, ModelRole{FollowChat: true}) {
		t.Fatalf("inherited role retains stale settings: %+v", got)
	}
}

func TestModelRoutingMediaInspectionAndParameters(t *testing.T) {
	r, _, _ := routingTestRuntime(t, ModelRole{ProfileID: "a", Model: "m"})
	cfg := r.profileConfig("")
	cfg.ModelRoles[mediaSlotTTS] = ModelRole{ProfileID: "a", Model: "voice-a", RoutingStrategy: "round_robin", Params: map[string]string{"voice": "speaker"}, Fallbacks: []ModelRole{{ProfileID: "b", Model: "voice-b"}, {ProfileID: "c", Model: "voice-c", Standby: true}, {ProfileID: "d", Model: "voice-d", Disabled: true}}}
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	for _, want := range []string{"voice-a", "voice-b"} {
		for range 2 {
			if len(r.mediaSlotRoutes(context.Background(), mediaSlotTTS)) != 3 {
				t.Fatal("disabled media route remained visible")
			}
		}
		routes := r.resolveMediaSlotRoutes(context.Background(), mediaSlotTTS, true)
		if len(routes) != 3 || routes[0].Model != want || routes[0].Params["voice"] != "speaker" || routes[2].Model != "voice-c" {
			t.Fatalf("routes=%+v want=%s", routes, want)
		}
	}
}

func TestModelRoutingRegistryBackgroundMemory(t *testing.T) {
	role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "round_robin", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m"}}}
	r, _, _ := routingTestRuntime(t, role)
	cfg := r.profileConfig("")
	cfg.ModelRoles["background"] = role
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	r.llmStore.(*stubLLMProfileStore).set.Profiles[3].Group = "memory"
	ctx := withLLMUsagePurpose(context.Background(), PurposeMemoryExtract)
	for _, want := range []string{"a", "b"} {
		got, err := r.runLLMMemoryProvider(ctx, func(provider LLMProvider) (string, error) {
			response, err := provider.Generate(ctx, llm.GenerateRequest{})
			if err != nil {
				return "", err
			}
			return response.Text, nil
		})
		if err != nil || got != want {
			t.Fatalf("memory=%s err=%v want=%s", got, err, want)
		}
	}
}
