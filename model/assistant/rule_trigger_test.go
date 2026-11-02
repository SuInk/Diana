package assistant

import (
	"context"
	"encoding/json"
	"strings"
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

func TestBotConfigSwitchesProactiveMode(t *testing.T) {
	base := BotConfig{ID: "a", OwnerID: "owner", Participation: &ParticipationPreferences{Desire: 75, RelevanceLevel: "on", ChatLevel: "low", RuleChance: 30, CooldownSeconds: 15}}
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{base}})
	r.SetGroupConfigStore(&testWritableGroupConfigStore{})
	event := MessageEvent{ProfileID: "a", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "owner"}
	tool := newDianaBotConfigTool(r, event)
	run := func(input map[string]any) map[string]any {
		t.Helper()
		raw, err := tool.Run(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if mode := run(map[string]any{"operation": "get"})["proactive"].(map[string]any)["mode"]; mode != "semantic" {
		t.Fatalf("legacy config must report semantic, got %v", mode)
	}
	out := run(map[string]any{"operation": "update", "proactive_mode": "rules", "rule_chance": 10})
	if p := out["proactive"].(map[string]any); p["mode"] != "rules" || p["rule_chance"] != float64(10) {
		t.Fatalf("proactive view %v", p)
	}
	actual := r.effectiveConfigForEvent(event)
	if !actual.ruleTriggerEnabled() || actual.Participation.RuleChance != 10 || actual.Participation.ChatLevel != "low" || actual.Participation.CooldownSeconds != 15 {
		t.Fatalf("rules not saved or other values lost: %+v", actual.Participation)
	}
	if r.ProfileConfig("a").ruleTriggerEnabled() {
		t.Fatal("group update changed robot default")
	}
	// 规则模式不看 chat_level，关话痨必须把概率归零，恢复时给回默认概率。
	run(map[string]any{"operation": "update", "desire_level": "off"})
	if got := r.effectiveConfigForEvent(event).Participation.RuleChance; got != 0 {
		t.Fatalf("desire off left rule chance %d", got)
	}
	run(map[string]any{"operation": "update", "desire_level": "low"})
	if got := r.effectiveConfigForEvent(event).Participation.RuleChance; got != defaultRuleTriggerChance {
		t.Fatalf("desire low restored chance %d", got)
	}
	for _, input := range []map[string]any{
		{"operation": "update", "proactive_mode": "random"},
		{"operation": "update", "rule_chance": 101},
		{"operation": "update", "rule_chance": 1.5},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("invalid update accepted: %v", input)
		}
	}
	run(map[string]any{"operation": "update", "proactive_mode": "semantic"})
	if r.effectiveConfigForEvent(event).ruleTriggerEnabled() {
		t.Fatal("switch back to semantic failed")
	}
}

func TestReplyTriggerCapabilityExplainsBothLayers(t *testing.T) {
	for _, document := range coreCapabilityDocuments {
		if document.ID != "core:reply-trigger" {
			continue
		}
		for _, want := range []string{"proactive_mode", "rules", "rule_chance", "省 Token", "trigger_word_mode", "loose"} {
			if !strings.Contains(document.Title+document.Content, want) {
				t.Fatalf("missing %q", want)
			}
		}
		return
	}
	t.Fatal("core:reply-trigger missing")
}

// 生产上问「触发模式有哪些」「闲聊触发模式」时，检索结果第一条要是这篇。
func TestReplyTriggerCapabilityRanksFirst(t *testing.T) {
	for _, query := range []string{"触发模式 触发词 trigger_mode", "闲聊触发 主动插话 chat_level desire_level 回复欲望 档位区别 cooldown", "省 token 不调用模型"} {
		hits := retrieveCapabilityDocuments(query, coreCapabilityDocuments, 3)
		if len(hits) == 0 || hits[0].ID != "core:reply-trigger" {
			t.Fatalf("%q top hits %v", query, hits)
		}
	}
}
