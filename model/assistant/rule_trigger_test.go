package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRuleTriggerRoutingWithoutModel(t *testing.T) {
	for _, chance := range []int{0, 100} {
		cfg := BotConfig{Participation: &ParticipationPreferences{TriggerMode: "rules", RuleChance: chance, CooldownSeconds: 30}}
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { t.Fatal("rules must not call routing model"); return nil, nil })
		event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "hello"}
		route := func() bool {
			_, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
			return allowed
		}
		if got := route(); got != (chance == 100) {
			t.Fatalf("chance %d allowed %t", chance, got)
		}
		r.markChatInReplied(event)
		if route() {
			t.Fatal("cooldown ignored")
		}
		r.chatInLastReplyAt = nil
		r.noteGroupStop(event, "stop", time.Now())
		if route() {
			t.Fatal("group stop ignored")
		}
	}
}

func TestRuleTriggerStableSample(t *testing.T) {
	e := MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "m"}
	for chance := 0; chance <= 100; chance++ {
		if ruleTriggerSample(e, chance) != ruleTriggerSample(e, chance) {
			t.Fatal("unstable")
		}
	}
	if ruleTriggerSample(e, 0) || !ruleTriggerSample(e, 100) {
		t.Fatal("probability endpoints")
	}
	e.MessageID = ""
	if ruleTriggerSample(e, 100) {
		t.Fatal("missing ID must not sample")
	}
}

func TestRuleTriggerConfigRoundTrip(t *testing.T) {
	p := &ParticipationPreferences{TriggerMode: "rules", RuleChance: 15, CooldownSeconds: 45}
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
	if *restored.Participation != *p {
		t.Fatalf("roundtrip: %+v", restored.Participation)
	}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"custom": {GroupID: "custom", Participation: &ParticipationPreferences{TriggerMode: "semantic"}}}})
	if !r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "inherit"}).ruleTriggerEnabled() {
		t.Fatal("inherit")
	}
	if r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "custom"}).ruleTriggerEnabled() {
		t.Fatal("override")
	}
	if copyParticipation(&ParticipationPreferences{TriggerMode: "unknown", RuleChance: 101}).RuleChance != 100 {
		t.Fatal("clamp")
	}
}

func TestRuleTriggerDirectRequestsIgnoreRandomCooldown(t *testing.T) {
	cfg := BotConfig{BotAccount: "42", GroupTriggers: []string{"Diana"}, Participation: &ParticipationPreferences{TriggerMode: "rules", RuleChance: 0, CooldownSeconds: 30}}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", SelfID: "42"}
	r.markChatInReplied(e)
	if !r.shouldHandleChatTrigger(e, "Diana 帮我看看") {
		t.Fatal("alias must directly trigger")
	}
	e.ToMe = true
	if !r.shouldHandleChatTrigger(e, "你好") {
		t.Fatal("mention must directly trigger")
	}
	e.ToMe = false
	e.Quoted = &QuotedMessage{MessageID: "old", UserID: "42", GroupID: "g"}
	if !r.shouldHandleChatTrigger(e, "你再确认下") {
		t.Fatal("quote must directly trigger")
	}
	e.Quoted = nil
	if r.shouldHandleChatTrigger(e, "普通群聊") {
		t.Fatal("ordinary chat must not directly trigger")
	}
}

func TestRuleTriggerKeepsAdmission(t *testing.T) {
	cfg := BotConfig{Participation: &ParticipationPreferences{TriggerMode: "rules", RuleChance: 100}, ReplyGate: &ReplyGate{BlockedUsers: []string{"blocked"}}}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, e := range []MessageEvent{{Kind: EventKindGroup, GroupID: "g", UserID: "blocked", MessageID: "m"}, {Kind: EventKindPrivate, UserID: "u", MessageID: "m"}} {
		_, _, _, allowed := r.routeRuleReply(e, "hello", cfg)
		if allowed {
			t.Fatal("admission bypassed")
		}
	}
}

func TestRuleTriggerDefaultChancePreservesZero(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{{`{"trigger_mode":"rules"}`, 30}, {`{"trigger_mode":"rules","rule_chance":0}`, 0}, {`{"trigger_mode":"rules","rule_chance":15}`, 15}} {
		var p ParticipationPreferences
		if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
			t.Fatal(err)
		}
		if p.RuleChance != tc.want {
			t.Fatalf("%s: got %d want %d", tc.raw, p.RuleChance, tc.want)
		}
	}
	if got := (BotConfig{}).participationPreferences().RuleChance; got != 30 {
		t.Fatalf("legacy config default=%d", got)
	}
}
