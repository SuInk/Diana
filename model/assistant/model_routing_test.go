// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestModelRequestTimeoutConfigRoundTrip(t *testing.T) {
	for _, milliseconds := range []int64{0, 1250, int64((1<<63 - 1) / time.Millisecond)} {
		cfg := ConfigFromPayload(ConfigPayload{ModelRequestTimeoutMS: milliseconds, RequestTimeoutMS: 45000}, BotConfig{}).WithDefaults()
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
		if cfg.ModelRequestTimeout.Milliseconds() != milliseconds || cfg.RequestTimeout != 45*time.Second {
			t.Fatalf("cfg timeout=%v total=%v", cfg.ModelRequestTimeout, cfg.RequestTimeout)
		}
		payload := PayloadFromConfig(cfg)
		if payload.ModelRequestTimeoutMS != milliseconds {
			t.Fatalf("payload=%d", payload.ModelRequestTimeoutMS)
		}
		data, err := json.Marshal(NewProfileSet(cfg))
		if err != nil {
			t.Fatal(err)
		}
		var restored ProfileSet
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Profiles[0].ModelRequestTimeout != cfg.ModelRequestTimeout {
			t.Fatal("profile persistence lost timeout")
		}
	}
	for _, milliseconds := range []int64{-1, -9223372036854775808, int64((1<<63-1)/time.Millisecond) + 1, 9223372036854775807, 18446744073710} {
		cfg := ConfigFromPayload(ConfigPayload{ModelRequestTimeoutMS: milliseconds}, BotConfig{}).WithDefaults()
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted invalid milliseconds=%d", milliseconds)
		}
	}
	if DefaultBotConfig().WithDefaults().ModelRequestTimeout != 0 {
		t.Fatal("new timeout must default to zero")
	}
}

func TestModelRequestTimeoutBotScope(t *testing.T) {
	r := NewRuntime(BotConfig{ID: "a", ModelRequestTimeout: time.Second}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "a", ModelRequestTimeout: time.Second}, {ID: "b", ModelRequestTimeout: 2 * time.Second}}})
	r.llmFactory = func() (LLMProvider, error) {
		return modelTimeoutTestProvider{run: func(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("missing model deadline")
			}
			return &llm.GenerateResponse{Text: fmt.Sprint(time.Until(deadline).Round(time.Second))}, nil
		}}, nil
	}
	for _, id := range []string{"a", "b"} {
		ctx := withModelConfigEvent(context.Background(), MessageEvent{ProfileID: id})
		got, err := r.runRawLLMProviderForGroup(ctx, llm.GroupChat, func(provider LLMProvider) (string, error) {
			resp, err := provider.Generate(context.Background(), llm.GenerateRequest{})
			if err != nil {
				return "", err
			}
			return resp.Text, nil
		})
		want := "1s"
		if id == "b" {
			want = "2s"
		}
		if err != nil || got != want {
			t.Fatalf("bot=%s got=%s err=%v", id, got, err)
		}
	}
}

func TestModelRecoveryPolicyUsesBotConfig(t *testing.T) {
	configured := &ModelRecoveryPolicy{
		TransientRetries:         0,
		RetryDelayMilliseconds:   125,
		TransientCooldownSeconds: 3,
		StatusCooldownSeconds:    45,
		MaxCooldownSeconds:       300,
		RespectRetryAfter:        false,
	}
	r := NewRuntime(BotConfig{ID: "bot", ModelRecoveryPolicy: configured, ModelRoles: map[string]ModelRole{
		"chat": {ProfileID: "a", Model: "m"},
	}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	policy := r.modelRecoveryPolicyForContext(context.Background(), llm.GroupChat)
	if policy.transientRetries != 0 || policy.retryDelay != 125*time.Millisecond || policy.transientCooldown != 3*time.Second || policy.statusCooldown != 45*time.Second || policy.maxCooldown != 5*time.Minute || policy.respectRetryAfter {
		t.Fatalf("policy = %#v", policy)
	}
}

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
		{"smart", []string{"a", "a", "a", "a"}},
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
	// 503 没攒够连续失败不冷却：两次请求都先试 a、b（各含两次原地重试），再落到 c。
	if adapters["a"].calls != 6 || adapters["b"].calls != 6 || adapters["c"].calls != 2 || adapters["d"].calls != 0 {
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
	if role.Weight != 1000 || role.RoutingStrategy != "weighted" || role.Fallbacks[0].RoutingStrategy != "" || role.Fallbacks[0].RecoveryPolicy != nil {
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
	legacy := BotConfig{ModelRoles: map[string]ModelRole{
		"chat": {ProfileID: "a", Model: "m", RecoveryPolicy: &ModelRecoveryPolicy{TransientRetries: 9, RetryDelayMilliseconds: -1, TransientCooldownSeconds: 7, StatusCooldownSeconds: 8, MaxCooldownSeconds: 0}},
	}}.WithDefaults()
	if policy := legacy.ModelRecoveryPolicy; policy == nil || policy.TransientRetries != 3 || policy.RetryDelayMilliseconds != 0 || policy.MaxCooldownSeconds != 1 {
		t.Fatalf("normalized recovery policy=%+v", policy)
	}
	if legacy.ModelRoles["chat"].RecoveryPolicy != nil {
		t.Fatalf("legacy purpose policy was not removed: %+v", legacy.ModelRoles["chat"])
	}
	restoredConfig := ConfigFromPayload(PayloadFromConfig(legacy), BotConfig{}).WithDefaults()
	if !reflect.DeepEqual(restoredConfig.ModelRecoveryPolicy, legacy.ModelRecoveryPolicy) {
		t.Fatalf("recovery policy round trip=%+v want=%+v", restoredConfig.ModelRecoveryPolicy, legacy.ModelRecoveryPolicy)
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

func TestModelRoutingSmartHealthAndRetention(t *testing.T) {
	role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "smart", Fallbacks: []ModelRole{
		{ProfileID: "c", Model: "m", Standby: true}, {ProfileID: "b", Model: "m"}, {ProfileID: "d", Model: "m", Disabled: true},
	}}
	r, _, set := routingTestRuntime(t, role)
	roles := map[string]ModelRole{"chat": role}
	now := time.Unix(100, 0)
	r.llmCooldowns.now = func() time.Time { return now }
	check := func(want ...string) {
		t.Helper()
		profiles, err := r.scheduledRoleProfiles(context.Background(), PurposeReply, set, llm.GroupChat, roles)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, profile := range profiles {
			got = append(got, profile.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%v want=%v", got, want)
		}
		if len(r.modelRouteSchedules) != 0 {
			t.Fatal("smart consumed round-robin state")
		}
	}
	check("a", "b", "c")
	r.llmCooldowns.markFailure(set.Profiles[0], errors.New("429 Too Many Requests: quota exceeded"))
	check("b", "a", "c")
	now = now.Add(time.Minute)
	// 冷却已过但失败计数还在：仍按配置顺序先试主模型。
	check("a", "b", "c")
	static, err := r.roleBoundProfiles(PurposeReply, set, llm.GroupChat, roles)
	if err != nil || static[0].ID != "a" {
		t.Fatalf("static profiles=%v err=%v", static, err)
	}
	r.llmCooldowns.clear(set.Profiles[0])
	check("a", "b", "c")
	r.llmCooldowns.markFailure(set.Profiles[0], errors.New("HTTP 500"))
	// 偶发一次 500 只记数、未进冷却：主模型照样排第一，不被备用顶掉。
	check("a", "b", "c")
	now = now.Add(24 * time.Hour)
	check("a", "b", "c")
	if len(r.llmCooldowns.entries) != 1 {
		t.Fatal("test must retain expired entry physically")
	}
}

func TestModelRoutingSmartGroupKeepsConfigOrderAfterCooldown(t *testing.T) {
	r, _, set := routingTestRuntime(t, ModelRole{ProfileID: "a", Model: "m"})
	now := time.Unix(100, 0)
	r.llmCooldowns.now = func() time.Time { return now }
	for index, failures := range []int{2, 1, 1, 0} {
		for range failures {
			r.llmCooldowns.markFailure(set.Profiles[index], errors.New("HTTP 500"))
		}
	}
	now = now.Add(time.Minute)
	profiles, err := r.scheduledRoleProfiles(context.Background(), PurposeReply, set, llm.GroupChat, map[string]ModelRole{"chat": {Group: "pool", Model: "m", RoutingStrategy: "smart"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, profile := range profiles {
		got = append(got, profile.ID)
	}
	if !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("smart group=%v", got)
	}
}

func TestModelRoutingAllStrategiesShareHealthAvoidance(t *testing.T) {
	for _, strategy := range []string{"", "smart", "failover", "round_robin", "weighted"} {
		t.Run(strategy, func(t *testing.T) {
			role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: strategy, Fallbacks: []ModelRole{{ProfileID: "b", Model: "m"}, {ProfileID: "c", Model: "m", Standby: true}}}
			r, _, set := routingTestRuntime(t, role)
			r.llmCooldowns.markFailure(set.Profiles[0], errors.New("HTTP 429 retry after 60s"))
			r.llmCooldowns.markFailure(set.Profiles[1], errors.New("HTTP 429 retry after 40s"))
			run := func(want string) {
				t.Helper()
				got, err := r.runRawLLMProviderForGroup(context.Background(), llm.GroupChat, func(provider LLMProvider) (string, error) {
					response, err := provider.Generate(context.Background(), llm.GenerateRequest{})
					return response.Text, err
				})
				if err != nil || got != want {
					t.Fatalf("got=%q err=%v want=%q", got, err, want)
				}
			}
			run("c")
			r.llmCooldowns.markFailure(set.Profiles[2], errors.New("HTTP 429 retry after 20s"))
			run("c")
		})
	}
}

func TestModelRoutingSmartNormalizationAndUnsupportedMedia(t *testing.T) {
	role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: "smart", Fallbacks: []ModelRole{{ProfileID: "b", Model: "m"}}}
	for _, strategy := range []string{"", "smart", "failover", "round_robin", "weighted", "unknown"} {
		input := role
		input.RoutingStrategy = " " + strategy + " "
		normalized := normalizeModelRole(input)
		want := strategy
		if want == "unknown" {
			want = ""
		}
		if normalized.RoutingStrategy != want {
			t.Fatalf("strategy=%q got=%q", strategy, normalized.RoutingStrategy)
		}
		body, err := json.Marshal(normalized)
		if err != nil {
			t.Fatal(err)
		}
		var output ModelRole
		if err := json.Unmarshal(body, &output); err != nil || !reflect.DeepEqual(normalized, normalizeModelRole(output)) {
			t.Fatalf("round trip=%+v err=%v", output, err)
		}
	}
	for _, slot := range []string{llm.GroupImage, mediaSlotTTS, mediaSlotSTT, mediaSlotVideo} {
		t.Run(slot, func(t *testing.T) {
			roles := normalizeModelRoles(map[string]ModelRole{"chat": role, slot: role, "vision": role})
			if roles[slot].RoutingStrategy != "failover" || roles["vision"].RoutingStrategy != "smart" {
				t.Fatalf("roles=%+v", roles)
			}
			for _, inherited := range []bool{false, true} {
				r, _, set := routingTestRuntime(t, role)
				roles = map[string]ModelRole{"chat": role, slot: role}
				if inherited {
					roles[slot] = ModelRole{FollowChat: true}
				}
				r.llmCooldowns.markFailure(set.Profiles[0], errors.New("HTTP 500"))
				for range 2 {
					profiles, err := r.scheduledRoleProfiles(context.Background(), slot, set, slot, roles)
					if err != nil || len(profiles) != 2 || profiles[0].ID != "a" {
						t.Fatalf("inherited=%v profiles=%v err=%v", inherited, profiles, err)
					}
				}
				if len(r.modelRouteSchedules) != 0 {
					t.Fatal("unsupported smart entered weighted scheduling")
				}
			}
		})
	}
	r, _, set := routingTestRuntime(t, role)
	r.llmCooldowns.markFailure(set.Profiles[0], errors.New("429 Too Many Requests: quota exceeded"))
	profiles, err := r.scheduledRoleProfiles(context.Background(), PurposeMediaParse, set, llm.GroupVision, map[string]ModelRole{"chat": role, "vision": {FollowChat: true}})
	if err != nil || profiles[0].ID != "b" {
		t.Fatalf("vision inherited smart: %v %v", profiles, err)
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

func TestModelRoutingDefaultSmartAndExplicitFailover(t *testing.T) {
	for _, strategy := range []string{"", "smart", "failover", " smart ", "unknown"} {
		t.Run(strategy, func(t *testing.T) {
			role := ModelRole{ProfileID: "a", Model: "m", RoutingStrategy: strategy, Fallbacks: []ModelRole{{ProfileID: "c", Model: "m", Standby: true}, {ProfileID: "b", Model: "m"}}}
			r, _, set := routingTestRuntime(t, role)
			now := time.Unix(100, 0)
			r.llmCooldowns.now = func() time.Time { return now }
			// 主模型仍在冷却：智能先排备用，主备保持原列表、交给调用层跳过。
			r.llmCooldowns.markFailure(set.Profiles[0], errors.New("429 Too Many Requests: quota exceeded"))
			for _, normalized := range []bool{false, true} {
				roles := map[string]ModelRole{"chat": role, "vision": {FollowChat: true}}
				if normalized {
					roles = normalizeModelRoles(roles)
				}
				profiles, err := r.scheduledRoleProfiles(context.Background(), PurposeMediaParse, set, llm.GroupVision, roles)
				want := "b"
				if strategy == "failover" {
					want = "a"
				}
				if err != nil || len(profiles) != 3 || profiles[0].ID != want {
					t.Fatalf("normalized=%v profiles=%v err=%v", normalized, profiles, err)
				}
				if normalized {
					if roles["vision"].RoutingStrategy != "" || roles["chat"].Fallbacks[0].RoutingStrategy != "" {
						t.Fatalf("inherited/fallback strategy persisted: %+v", roles)
					}
				}
			}
		})
	}
	for _, key := range []string{"chat", "vision", "intent", llm.GroupImage, mediaSlotTTS, mediaSlotSTT, mediaSlotVideo} {
		role := ModelRole{ProfileID: "a", Model: "m"}
		normalized := normalizeModelRoles(map[string]ModelRole{key: role})[key]
		want := "smart"
		if !supportsSmartModelRouting(key) {
			want = "failover"
		}
		if normalized.RoutingStrategy != want || effectiveModelRoutingStrategy("", key) != want {
			t.Fatalf("key=%s normalized=%+v", key, normalized)
		}
	}
}
