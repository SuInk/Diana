package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	for percent := 0; percent <= 100; percent++ {
		chance := float64(percent) / 100
		if ruleTriggerSample(e, chance) != ruleTriggerSample(e, chance) {
			t.Fatal("unstable")
		}
	}
	if ruleTriggerSample(e, 0) || !ruleTriggerSample(e, 1) {
		t.Fatal("probability endpoints")
	}
	e.MessageID = ""
	if ruleTriggerSample(e, 1) {
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
		_, _, _, allowed := r.routeRuleReply([]proactiveReplyCandidate{{Event: e, Text: "hello"}}, cfg)
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
		{"operation": "update", "proactive_mode": "fixed"},
		{"operation": "update", "rule_chance": 101},
		{"operation": "update", "rule_chance": 1.5},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("invalid update accepted: %v", input)
		}
	}
	out = run(map[string]any{"operation": "update", "proactive_mode": "random"})
	if p := out["proactive"].(map[string]any); p["mode"] != "random" || !r.effectiveConfigForEvent(event).ruleTriggerEnabled() {
		t.Fatalf("random mode not saved: %v", p)
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

func TestRuleChatChanceAdjustsByLocalSignals(t *testing.T) {
	const at = int64(1_000_000)
	now := time.Unix(at, 0)
	msg := func(id, user string, ago int64, text string) MessageEvent {
		return MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: id, UserID: user, Time: at - ago, RawMessage: text}
	}
	bot := func(id string, ago int64) MessageEvent {
		return MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: id, UserID: "bot", Time: at - ago, Outbound: true}
	}
	current := msg("cur", "u1", 0, "")
	// 一段普通节奏：三个人隔几十秒说一句，上一条 40 秒前。
	normal := []MessageEvent{msg("a", "u2", 200, "x"), msg("b", "u3", 120, "y"), msg("c", "u2", 40, "z")}
	chance := func(text string, history []MessageEvent, fatigue float64) float64 {
		got, _ := ruleChatChance(30, current, text, history, "bot", fatigue, now)
		return got
	}

	if got := chance("今天食堂的菜还行", normal, 0); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("plain message should keep base chance, got %v", got)
	}
	for _, text := range []string{"今天去哪吃饭？", "有人在吗", "你呢~"} {
		if got := chance(text, normal, 0); got <= 0.3 {
			t.Fatalf("question %q should raise chance, got %v", text, got)
		}
	}
	for _, text := range []string{"哈哈哈", "hhhh", "666", "好的", "[图片]", "😂😂😂", "👍🏻", "草。", "OK!"} {
		if got := chance(text, normal, 0); got >= 0.3 {
			t.Fatalf("low-content %q should lower chance, got %v", text, got)
		}
	}
	if got := chance("下班了", normal, 0); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("three distinct characters is not low content, got %v", got)
	}
	if got := chance("今天食堂的菜还行", nil, 0); got <= 0.3 {
		t.Fatalf("first message after silence should raise chance, got %v", got)
	}
	if got := chance("今天食堂的菜还行", []MessageEvent{msg("old", "u2", 900, "x")}, 0); got <= 0.3 {
		t.Fatalf("quiet gap should raise chance, got %v", got)
	}

	var busy []MessageEvent
	for i := 0; i < 23; i++ {
		busy = append(busy, msg(fmt.Sprintf("b%d", i), fmt.Sprintf("u%d", 2+i%5), int64(100-i*4), "刷"))
	}
	if got := chance("今天食堂的菜还行", busy, 0); math.Abs(got-0.3*0.25) > 1e-9 {
		t.Fatalf("24 messages in 2 minutes should scale to 6/24, got %v", got)
	}

	duo := []MessageEvent{msg("d1", "u2", 60, "x"), msg("d2", "u1", 45, "y"), msg("d3", "u2", 30, "z")}
	if got := chance("今天食堂的菜还行", duo, 0); got >= 0.3 {
		t.Fatalf("two-person exchange should lower chance, got %v", got)
	}

	// 机器人刚回了 u1，u1 没引用、接着说：接着跟机器人对话。
	partner := []MessageEvent{msg("p1", "u2", 100, "x"), msg("p2", "u1", 60, "那个怎么弄"), bot("p3", 40), msg("p4", "u3", 20, "插一句")}
	if got := chance("1TB", partner, 0); math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("continuing a conversation with the bot should reach 2.5x, got %v", got)
	}
	// 机器人引用了 u1 回复：即使中间最近的真人是别人，也认 u1。
	quotedBot := bot("q2", 30)
	quotedBot.Quoted = &QuotedMessage{UserID: "u1"}
	if got := chance("1TB", []MessageEvent{msg("q1", "u2", 50, "x"), quotedBot}, 0); got <= 0.6 {
		t.Fatalf("bot quoting this user should count as continuation, got %v", got)
	}
	// 机器人刚说过话，但回的是别人：只略微抬高。
	other := []MessageEvent{msg("o1", "u3", 200, "x"), msg("o2", "u2", 60, "y"), bot("o3", 30)}
	if got := chance("今天食堂的菜还行", other, 0); got <= 0.3 || got > 0.4 {
		t.Fatalf("bot spoke to someone else should only nudge chance, got %v", got)
	}
	// 太久以前的对话不算。
	stale := []MessageEvent{msg("s1", "u1", 400, "x"), bot("s2", 300), msg("s3", "u2", 100, "y")}
	if got := chance("今天食堂的菜还行", stale, 0); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("stale conversation should not count, got %v", got)
	}

	if got := chance("今天食堂的菜还行", normal, 1); got >= 0.3*0.31 {
		t.Fatalf("full fatigue should cut chance to 30%%, got %v", got)
	}

	if got := chance("今天去哪吃饭？", nil, 0); got > 0.6+1e-9 {
		t.Fatalf("chance must be capped at twice the base, got %v", got)
	}
	if got, _ := ruleChatChance(100, current, "哈哈", busy, "bot", 1, now); got != 1 {
		t.Fatalf("100%% means always, got %v", got)
	}
	if got, _ := ruleChatChance(0, current, "在吗？", nil, "bot", 0, now); got != 0 {
		t.Fatalf("0%% means never, got %v", got)
	}
}

func TestRuleTriggerReasonShowsAdjustedChance(t *testing.T) {
	cfg := BotConfig{Participation: &ParticipationPreferences{TriggerMode: "rules", RuleChance: 30, CooldownSeconds: 30}}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { t.Fatal("rules must not call routing model"); return nil, nil })
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "哈哈哈", Time: time.Now().Unix()}
	routed, _, _, _ := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
	if !strings.Contains(routed.routingReason, "基础概率 30%") || !strings.Contains(routed.routingReason, "内容很少") {
		t.Fatalf("reason should explain the adjustment: %s", routed.routingReason)
	}
}

// 连发攒成一批时，前面的问句不能被最后一句「哈哈」盖掉。
func TestRuleTriggerBatchPicksHighestChance(t *testing.T) {
	cfg := BotConfig{Participation: &ParticipationPreferences{TriggerMode: "rules", RuleChance: 30, CooldownSeconds: 30}}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now().Unix()
	question := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "q", RawMessage: "这个怎么弄？", Time: now - 2}
	filler := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "f", RawMessage: "哈哈", Time: now}
	routed, text, _, _ := r.routeRuleReply([]proactiveReplyCandidate{{Event: question, Text: question.RawMessage}, {Event: filler, Text: filler.RawMessage}}, cfg)
	if routed.MessageID != "q" || text != question.RawMessage || !strings.Contains(routed.routingReason, "连发 2 条里概率最高的第 1 条") {
		t.Fatalf("batch should be judged by the question: %s %s", routed.MessageID, routed.routingReason)
	}
}

func TestRandomTriggerModeUsesFixedChance(t *testing.T) {
	for _, chance := range []int{0, 100} {
		cfg := BotConfig{Participation: &ParticipationPreferences{TriggerMode: "random", RuleChance: chance, CooldownSeconds: 30}}
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { t.Fatal("random mode must not call routing model"); return nil, nil })
		event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "哈哈", Time: time.Now().Unix()}
		routed, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
		if allowed != (chance == 100) {
			t.Fatalf("chance %d allowed %t: %s", chance, allowed, routed.routingReason)
		}
		if chance == 100 && !strings.Contains(routed.routingReason, "普通模式：固定接话概率 100%") {
			t.Fatalf("reason: %s", routed.routingReason)
		}
	}
	if got := copyParticipation(&ParticipationPreferences{TriggerMode: "random"}).TriggerMode; got != "random" {
		t.Fatalf("random mode must survive normalization, got %q", got)
	}
}
