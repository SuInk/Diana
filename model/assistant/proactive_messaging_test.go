// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func proactiveHuman(at time.Time, text string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001", Time: at.Unix(), RawMessage: text}
}

func proactiveBot(at time.Time, initiative string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "30001", Outbound: true, Time: at.Unix(), Initiative: initiative}
}

func groupRules() proactiveRules {
	return proactiveRules{Idle: 2 * time.Hour, DailyLimit: 2}
}

func TestEvaluateProactiveGroupNeedsWarmChatThenSilence(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.Local)
	warm := []MessageEvent{
		proactiveHuman(now.Add(-4*time.Hour), "a"),
		proactiveHuman(now.Add(-3*time.Hour-30*time.Minute), "b"),
		proactiveHuman(now.Add(-3*time.Hour), "c"),
	}
	plan, reason, ok := evaluateProactive(warm, "30001", groupRules(), now, "k")
	if !ok {
		t.Fatalf("warm group silent for 3h should be due, got %q", reason)
	}
	if plan.Unanswered != 0 || plan.Watermark != warm[2].Time {
		t.Fatalf("plan = %+v", plan)
	}

	if _, _, ok := evaluateProactive(warm[2:], "30001", groupRules(), now, "k"); ok {
		t.Fatal("one lonely message is not a conversation that went quiet")
	}
	if _, _, ok := evaluateProactive(warm, "30001", groupRules(), now.Add(-90*time.Minute), "k"); ok {
		t.Fatal("silence shorter than the idle threshold must not trigger")
	}
	if _, _, ok := evaluateProactive(warm, "30001", groupRules(), now.Add(3*24*time.Hour), "k"); ok {
		t.Fatal("a group silent for days is dead, not quiet")
	}
	if _, _, ok := evaluateProactive(nil, "30001", groupRules(), now, "k"); ok {
		t.Fatal("empty history must not trigger")
	}
}

func TestEvaluateProactiveBacksOffWhenUnanswered(t *testing.T) {
	now := time.Date(2026, 11, 8, 20, 0, 0, 0, time.Local)
	history := []MessageEvent{
		proactiveHuman(now.Add(-8*time.Hour), "a"),
		proactiveHuman(now.Add(-7*time.Hour-50*time.Minute), "b"),
		proactiveHuman(now.Add(-7*time.Hour-40*time.Minute), "c"),
		// 两条气泡同属一次开口。
		proactiveBot(now.Add(-5*time.Hour), "i1"),
		proactiveBot(now.Add(-5*time.Hour+3*time.Second), "i1"),
	}
	if _, reason, ok := evaluateProactive(history, "30001", groupRules(), now, "k"); ok || !strings.Contains(reason, "没人接") {
		t.Fatalf("group stops after one unanswered initiative, got ok=%v reason=%q", ok, reason)
	}

	owner := proactiveRules{Owner: true, Idle: 2 * time.Hour, DailyLimit: 5}
	// 主人那边可以再追一次，但要等的时长翻倍：离上次开口才 3 小时，不够 4 小时。
	ownerHistory := append([]MessageEvent(nil), history...)
	ownerHistory[3].Time = now.Add(-3 * time.Hour).Unix()
	ownerHistory[4].Time = now.Add(-3 * time.Hour).Unix()
	if _, _, ok := evaluateProactive(ownerHistory, "30001", owner, now, "k"); ok {
		t.Fatal("second owner nudge must wait for the doubled idle time")
	}
	plan, reason, ok := evaluateProactive(ownerHistory, "30001", owner, now.Add(90*time.Minute), "k")
	if !ok || plan.Unanswered != 1 {
		t.Fatalf("owner nudge after doubled idle should be due, plan=%+v reason=%q", plan, reason)
	}

	// 有人接了话，计数清零。
	answered := append(append([]MessageEvent(nil), history...),
		proactiveHuman(now.Add(-4*time.Hour-50*time.Minute), "d"),
		proactiveHuman(now.Add(-4*time.Hour-40*time.Minute), "e"),
		proactiveHuman(now.Add(-4*time.Hour-30*time.Minute), "f"),
	)
	if _, reason, ok := evaluateProactive(answered, "30001", groupRules(), now, "k"); !ok {
		t.Fatalf("a reply resets the backoff, got %q", reason)
	}
}

func TestEvaluateProactiveDailyLimitCountsInitiativesNotBubbles(t *testing.T) {
	now := time.Date(2026, 11, 8, 22, 0, 0, 0, time.Local)
	history := []MessageEvent{
		proactiveBot(now.Add(-9*time.Hour), "i1"),
		proactiveBot(now.Add(-9*time.Hour+time.Second), "i1"),
		proactiveHuman(now.Add(-8*time.Hour), "a"),
		proactiveHuman(now.Add(-7*time.Hour), "b"),
		proactiveHuman(now.Add(-6*time.Hour), "c"),
	}
	rules := groupRules()
	rules.DailyLimit = 2
	if _, reason, ok := evaluateProactive(history, "30001", rules, now, "k"); !ok {
		t.Fatalf("two bubbles of one initiative count once, got %q", reason)
	}
	rules.DailyLimit = 1
	if _, reason, ok := evaluateProactive(history, "30001", rules, now, "k"); ok || !strings.Contains(reason, "次数") {
		t.Fatalf("daily limit reached, got ok=%v reason=%q", ok, reason)
	}
}

func TestProactiveJitterStaysWithinTenPercentAndIsStable(t *testing.T) {
	base := 2 * time.Hour
	for _, key := range []string{"a", "b", "group:1|1700000000", "private:2|1700000300"} {
		got := proactiveJitter(base, key)
		if got < base*9/10 || got > base*11/10 {
			t.Fatalf("jitter(%q) = %v out of range", key, got)
		}
		if again := proactiveJitter(base, key); again != got {
			t.Fatalf("jitter not stable for %q: %v vs %v", key, got, again)
		}
	}
}

func TestProactiveQuietHoursWrapMidnight(t *testing.T) {
	settings := (&ProactiveMessaging{Enabled: true}).normalized()
	at := func(hour, minute int) time.Time { return time.Date(2026, 11, 8, hour, minute, 0, 0, time.UTC) }
	for _, quiet := range []time.Time{at(23, 0), at(2, 30), at(7, 59)} {
		if !proactiveQuietAt(settings, quiet) {
			t.Fatalf("%v should be quiet", quiet)
		}
	}
	for _, awake := range []time.Time{at(8, 0), at(12, 0), at(22, 59)} {
		if proactiveQuietAt(settings, awake) {
			t.Fatalf("%v should not be quiet", awake)
		}
	}
	off := &ProactiveMessaging{QuietStart: "00:00", QuietEnd: "00:00"}
	if proactiveQuietAt(off.normalized(), at(3, 0)) {
		t.Fatal("equal start and end disables quiet hours")
	}
}

func TestProactiveMessagingNormalizeAndValidate(t *testing.T) {
	got := (&ProactiveMessaging{Enabled: true, GroupIDs: []string{" 10001 ", "", "10001", "10002"}}).normalized()
	if strings.Join(got.GroupIDs, ",") != "10001,10002" {
		t.Fatalf("groups = %#v", got.GroupIDs)
	}
	if got.GroupIdleMinutes != defaultProactiveGroupIdleMinutes || got.OwnerIdleHours != defaultProactiveOwnerIdleHours || got.DailyLimit != defaultProactiveDailyLimit {
		t.Fatalf("defaults not applied: %+v", got)
	}
	var nilSettings *ProactiveMessaging
	if nilSettings.normalized() != nil || nilSettings.validate() != nil {
		t.Fatal("nil settings stay nil and valid")
	}
	for _, bad := range []*ProactiveMessaging{
		{GroupIdleMinutes: 5},
		{OwnerIdleHours: 1000},
		{DailyLimit: 50},
		{QuietStart: "25:00", QuietEnd: "08:00"},
		{QuietStart: "23:00"},
		{GroupIDs: []string{"摸鱼群"}},
	} {
		if bad.validate() == nil {
			t.Fatalf("expected validation error for %+v", bad)
		}
	}
}

func TestNormalizeProactiveOutputHonoursSkip(t *testing.T) {
	for _, raw := range []string{"[SKIP]", "  [skip]\n", "嗯……[SKIP]", ""} {
		if got := normalizeProactiveOutput(raw); got != "" {
			t.Fatalf("normalize(%q) = %q, want skip", raw, got)
		}
	}
	if got := normalizeProactiveOutput("  「昨天那家店后来去了吗」 "); got != "昨天那家店后来去了吗" {
		t.Fatalf("normalize trimmed wrong: %q", got)
	}
}

// newProactiveRuntime 默认关掉账号安全审核：桩模型回不出审核格式，审核会判失败。
// 审核那条路单独测。
func newProactiveRuntime(t *testing.T, text string, safetyAudit ...bool) (*Runtime, *recordingChannel, *welcomeStubLLM) {
	t.Helper()
	channel := &recordingChannel{}
	stub := &welcomeStubLLM{text: text}
	runtime := newLLMWelcomeRuntime(BotConfig{
		SystemPrompt:             "一只高冷的猫娘",
		ReplySafetyMasterEnabled: boolPointer(len(safetyAudit) > 0 && safetyAudit[0]),
		ProactiveMessaging:       &ProactiveMessaging{Enabled: true, GroupIDs: []string{"10001"}},
	}, channel, stub)
	return runtime, channel, stub
}

func rememberWarmGroup(runtime *Runtime, now time.Time) {
	for i, text := range []string{"周末去爬山吗", "去哪座", "听说香山红叶正好"} {
		runtime.remember(MessageEvent{
			Kind: EventKindGroup, GroupID: "10001", UserID: "2000" + string(rune('1'+i)), SenderName: "群友",
			MessageID: "m" + string(rune('1'+i)), RawMessage: text,
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
			Time:     now.Add(-3*time.Hour + time.Duration(i)*time.Minute).Unix(),
		})
	}
}

func TestProactiveInitiativeSendsAndMarksHistory(t *testing.T) {
	runtime, channel, stub := newProactiveRuntime(t, "香山那边后来有人去成了吗")
	now := time.Now()
	rememberWarmGroup(runtime, now)
	target := proactiveTarget{
		event: MessageEvent{Kind: EventKindGroup, MessageType: "group", GroupID: "10001", Time: now.Unix()},
		rules: groupRules(),
	}
	plan, ok := runtime.proactivePlanFor(target, now)
	if !ok {
		t.Fatal("warm group silent for 3h should be due")
	}
	runtime.runProactiveInitiative(context.Background(), target, plan)

	if len(channel.sent) != 1 || channel.sent[0].Text != "香山那边后来有人去成了吗" || channel.sent[0].GroupID != "10001" {
		t.Fatalf("sent = %#v, events = %+v", channel.sent, runtime.recent)
	}
	dump := welcomeRequestDump(stub.lastRequest())
	for _, want := range []string{"一只高冷的猫娘", "听说香山红叶正好", "【内部事件，不是任何人发来的消息】", "[SKIP]"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("proactive request missing %q:\n%s", want, dump)
		}
	}
	history := runtime.proactiveRecentHistory(target.event)
	last := history[len(history)-1]
	if !last.Outbound || last.Initiative == "" {
		t.Fatalf("sent message must be remembered as an initiative: %+v", last)
	}
	// 没人接，下一次不会再来。
	if _, ok := runtime.proactivePlanFor(target, now.Add(6*time.Hour)); ok {
		t.Fatal("unanswered initiative must block the next one")
	}
}

func TestProactiveInitiativeSkipSendsNothing(t *testing.T) {
	runtime, channel, stub := newProactiveRuntime(t, "[SKIP]")
	now := time.Now()
	rememberWarmGroup(runtime, now)
	target := proactiveTarget{
		event: MessageEvent{Kind: EventKindGroup, MessageType: "group", GroupID: "10001", Time: now.Unix()},
		rules: groupRules(),
	}
	plan, ok := runtime.proactivePlanFor(target, now)
	if !ok {
		t.Fatal("should be due")
	}
	runtime.runProactiveInitiative(context.Background(), target, plan)
	if len(channel.sent) != 0 {
		t.Fatalf("skip must not send: %#v", channel.sent)
	}
	if stub.callCount() != 1 {
		t.Fatalf("llm calls = %d", stub.callCount())
	}
	// 进程内冷却：同一段冷场不会每五分钟问一次。
	key := "k"
	if !runtime.proactive.claim(key, now, plan.Required) {
		t.Fatal("first claim should succeed")
	}
	runtime.proactive.release(key)
	if runtime.proactive.claim(key, now.Add(10*time.Minute), plan.Required) {
		t.Fatal("re-asking within the idle window must be refused")
	}
}

func TestProactiveInitiativeDroppedWhenSomeoneSpeaksMeanwhile(t *testing.T) {
	runtime, channel, _ := newProactiveRuntime(t, "香山那边后来有人去成了吗")
	now := time.Now()
	rememberWarmGroup(runtime, now)
	target := proactiveTarget{
		event: MessageEvent{Kind: EventKindGroup, MessageType: "group", GroupID: "10001", Time: now.Unix()},
		rules: groupRules(),
	}
	plan, ok := runtime.proactivePlanFor(target, now)
	if !ok {
		t.Fatal("should be due")
	}
	// 生成期间有人说话：用一条比水位新的消息模拟。
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20009", MessageID: "m9", RawMessage: "我回来了",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "我回来了"}}}, Time: now.Unix()})
	runtime.runProactiveInitiative(context.Background(), target, plan)
	if len(channel.sent) != 0 {
		t.Fatalf("superseded initiative must not send: %#v", channel.sent)
	}
}

func TestProactiveInitiativeNotSentWhenSafetyAuditFails(t *testing.T) {
	runtime, channel, _ := newProactiveRuntime(t, "香山那边后来有人去成了吗", true)
	now := time.Now()
	rememberWarmGroup(runtime, now)
	target := proactiveTarget{
		event: MessageEvent{Kind: EventKindGroup, MessageType: "group", GroupID: "10001", Time: now.Unix()},
		rules: groupRules(),
	}
	plan, ok := runtime.proactivePlanFor(target, now)
	if !ok {
		t.Fatal("should be due")
	}
	runtime.runProactiveInitiative(context.Background(), target, plan)
	if len(channel.sent) != 0 {
		t.Fatalf("an initiative whose safety audit could not run must not be sent: %#v", channel.sent)
	}
	if len(runtime.recent) == 0 || runtime.recent[0].Outcome != "initiative_blocked" {
		t.Fatalf("events = %+v", runtime.recent)
	}
}
