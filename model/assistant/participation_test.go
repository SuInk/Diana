package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestParticipationRoundTripAndGroupInheritance(t *testing.T) {
	p := &ParticipationPreferences{Desire: 83, Social: 91, Followup: 42, Restraint: 19, Information: 0, CooldownSeconds: 45}
	cfg := BotConfig{Participation: p}.WithDefaults()
	data, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	restored := ConfigFromPayload(payload, cfg).WithDefaults()
	if got := restored.participationPreferences(); got != *p {
		t.Fatalf("round trip: %+v", got)
	}
	restored.Participation.Desire = 1
	if p.Desire != 83 {
		t.Fatal("configuration aliases caller memory")
	}
	r := NewRuntime(cfg, nil, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"custom":  {GroupID: "custom", Participation: &ParticipationPreferences{Desire: 0, Social: 81}},
		"preset":  {GroupID: "preset", ResponseMode: ResponseModeSuperActive},
		"inherit": {GroupID: "inherit"},
	}})
	if got := r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "inherit"}).participationPreferences(); got != *p {
		t.Fatalf("inheritance: %+v", got)
	}
	if got := r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "custom"}).participationPreferences(); got.Desire != 0 || got.Social != 81 {
		t.Fatalf("group override: %+v", got)
	}
	if got := r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "preset"}).participationPreferences(); got.Desire != 100 {
		t.Fatalf("legacy group preset overridden by bot custom: %+v", got)
	}
}

func TestParticipationIndependentCooldownDefaults(t *testing.T) {
	for _, level := range ChatInLevels() {
		if got := (BotConfig{ChatInLevel: level}).participationPreferences().CooldownSeconds; got != 30 {
			t.Fatalf("level=%s cooldown=%d", level, got)
		}
	}
	for _, tc := range []struct {
		input string
		want  int
	}{
		{`{"desire":100}`, 30},
		{`{"desire":100,"cooldown_seconds":0}`, 0},
		{`{"desire":100,"cooldown_seconds":45}`, 45},
	} {
		var preferences ParticipationPreferences
		if err := json.Unmarshal([]byte(tc.input), &preferences); err != nil {
			t.Fatal(err)
		}
		if preferences.CooldownSeconds != tc.want {
			t.Fatalf("input=%s cooldown=%d want=%d", tc.input, preferences.CooldownSeconds, tc.want)
		}
		data, err := json.Marshal(PayloadFromConfig(BotConfig{Participation: &preferences}))
		if err != nil {
			t.Fatal(err)
		}
		var payload ConfigPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		cfg := ConfigFromPayload(payload, BotConfig{}).WithDefaults()
		if got := cfg.participationPreferences().CooldownSeconds; got != tc.want {
			t.Fatalf("round trip cooldown=%d want=%d", got, tc.want)
		}
	}
}

func TestParticipationIsSoleIntentGate(t *testing.T) {
	cfg := BotConfig{ChatInLevel: ChatInLevelMax, ProactiveReplyChance: .01, ChatInThreshold: 1, ChatInCooldownSeconds: 600}.WithDefaults()
	settings := cfg.chatInSettings()
	decision := proactiveReplyDecision{ShouldReply: true, Category: "chat_in", Confidence: .01, Substantive: false}
	if !decision.allows(.99, settings) || settings.Chance != 1 || settings.Cooldown != 600*time.Second {
		t.Fatal("legacy gate still active")
	}
	decision.ShouldReply = false
	if decision.allows(0, settings) {
		t.Fatal("model silence overridden")
	}
	prompt := proactiveReplyRouterPromptForChatIn("旧规则：没有新信息不能发言", settings, false)
	if strings.Contains(prompt, "旧规则") || !strings.Contains(prompt, "本轮档位：max") {
		t.Fatal(prompt)
	}
	if got := copyParticipation(&ParticipationPreferences{Desire: -1, Social: 101}); got.Desire != 0 || got.Social != 100 {
		t.Fatal(got)
	}
}

func TestParticipationCooldownRouting(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_reply":true,"confidence":0.72,"category":"chat_in","substantive":false,"target_message_id":"m","reason":"适合自然接话"}`}
	r := NewRuntime(BotConfig{Participation: &ParticipationPreferences{Desire: 100, CooldownSeconds: 30}}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "茶喝完了"}
	route := func(e MessageEvent) bool {
		t.Helper()
		routed, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: e, Text: e.RawMessage}})
		t.Logf("INPUT=%q PROFILE=%s GROUP=%s MODEL_FIXTURE=%s ALLOWED=%t REASON=%s", e.RawMessage, e.ProfileID, e.GroupID, provider.reply, allowed, routed.routingReason)
		return allowed
	}
	if !route(event) || !route(event) {
		t.Fatal("unsent replies must not start cooldown")
	}
	r.markChatInReplied(event)
	if route(event) {
		t.Fatal("cooldown did not block casual reply")
	}
	other := event
	other.ProfileID = "bot-b"
	if !route(other) {
		t.Fatal("cooldown leaked to another robot")
	}
	other = event
	other.GroupID = "other"
	if !route(other) {
		t.Fatal("cooldown leaked to another group")
	}
	provider.reply = `{"should_reply":true,"confidence":0.72,"category":"needs_response","target_message_id":"m"}`
	if !route(event) {
		t.Fatal("cooldown blocked public question")
	}
	provider.reply = `{"should_reply":true,"confidence":0.72,"category":"bot_related","directed_at_bot":true,"target_message_id":"m"}`
	if !route(event) {
		t.Fatal("cooldown blocked directed reply")
	}
	provider.reply = `{"should_reply":true,"confidence":0.72,"category":"chat_in","target_message_id":"m"}`
	r.mu.Lock()
	r.chatInLastReplyAt[chatInCooldownKey(event)] = time.Now().Add(-31 * time.Second)
	r.mu.Unlock()
	if !route(event) {
		t.Fatal("expired cooldown still blocks")
	}
	if !r.chatInCooldownAllows(event, 0) {
		t.Fatal("zero must disable cooldown")
	}
}

func TestLiveParticipationPreferences(t *testing.T) {
	client := liveLLMClient(t)
	for _, tc := range []struct {
		name, input string
		level       ChatInLevel
		want        bool
		preferences *ParticipationPreferences
	}{
		{"max_tea", `{"candidates":[{"message_id":"105766","user_id":"u1","text":"今天把之前买得200g茉莉花茶"},{"message_id":"105767","user_id":"u1","text":"喝完了"}],"recent_messages":[],"last_bot_message":""}`, ChatInLevelMax, true, nil},
		{"off_tea", `{"candidates":[{"message_id":"105766","user_id":"u1","text":"今天把之前买得200g茉莉花茶"},{"message_id":"105767","user_id":"u1","text":"喝完了"}],"recent_messages":[],"last_bot_message":""}`, ChatInLevelOff, false, nil},
		{"max_stop", `{"candidates":[{"message_id":"3","user_id":"u1","text":"Diana，先别回复了，让我们自己聊。"}]}`, ChatInLevelMax, false, nil},
		{"off_direct_request", `{"candidates":[{"message_id":"4","user_id":"u1","text":"Diana，帮我解释一下茉莉花茶为什么有花香。","mentioned_bot":true}]}`, ChatInLevelOff, true, nil},
		{"legacy_custom_tea", `{"candidates":[{"message_id":"105766","user_id":"u1","text":"今天把之前买得200g茉莉花茶"},{"message_id":"105767","user_id":"u1","text":"喝完了"}],"recent_messages":[],"last_bot_message":""}`, ChatInLevelMax, true, &ParticipationPreferences{Desire: 100, Social: 0, Followup: 0, Restraint: 100, Information: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BotConfig{ChatInLevel: tc.level, Participation: tc.preferences}.WithDefaults()
			probe := &liveTopicProbe{LLMProvider: client, t: t}
			resp, err := probe.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: proactiveReplyRouterPromptForChatIn(cfg.ProactiveReplyRouterPrompt, cfg.chatInSettings(), false)}, {Role: llm.RoleUser, Content: tc.input}}})
			if err != nil {
				t.Fatal("real model failed; see redacted log")
			}
			d, ok := parseProactiveReplyDecision(resp.Text)
			if !ok || d.allows(1, cfg.chatInSettings()) != tc.want {
				t.Fatalf("decision=%+v parsed=%t want=%t", d, ok, tc.want)
			}
		})
	}
}
