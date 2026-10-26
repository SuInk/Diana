// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func repeatEvent(id, user, text string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "qq", SelfID: "42", GroupID: "123456", UserID: user, MessageID: id, RawMessage: text, SenderLevel: 40, SenderLevelLabel: "LV40", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}}
}

func TestRandomRepeaterChains(t *testing.T) {
	p := newRandomRepeater()
	now := time.Now()
	p.now = func() time.Time { return now }
	rolls := 0
	p.roll = func() int { rolls++; return 0 }
	settings := RandomRepeatConfig{Probability: 100, CooldownSeconds: 60}
	step := func(id, user, text, want string) {
		t.Helper()
		e := repeatEvent(id, user, text)
		p.observe(context.Background(), e)
		if got := p.takeReply(e, settings).text; got != want {
			t.Fatalf("%s: got %q want %q", id, got, want)
		}
	}
	step("1", "a", "安排", "")
	step("1", "b", "安排", "") // duplicate delivery cannot add another participant
	step("2", "a", "安排", "")
	step("3", "b", "安排", "安排")
	step("4", "c", "安排", "")
	now = now.Add(time.Minute)
	step("5", "d", "安排", "") // cooldown expiry cannot reopen the same chain
	step("6", "a", "好耶", "")
	step("7", "b", "好耶", "好耶")
	step("8", "a", "新内容", "")
	step("9", "b", "新内容", "") // new chain is still subject to cooldown
	if rolls != 2 {
		t.Fatalf("roll count = %d", rolls)
	}
	now = now.Add(24 * time.Hour)
	step("10", "c", "新内容", "新内容")
	step("11", "d", "新内容", "")
	step("12", "a", "下一轮", "")
	step("13", "b", "下一轮", "")
}

func TestRandomRepeaterProbabilityAndScope(t *testing.T) {
	for _, probability := range []int{0, 30, 100} {
		for _, roll := range []int{0, 29, 30, 99} {
			p := newRandomRepeater()
			p.roll = func() int { return roll }
			first := repeatEvent("1", "a", "安排")
			p.observe(context.Background(), first)
			second := repeatEvent("2", "b", "安排")
			for _, change := range []func(*MessageEvent){func(e *MessageEvent) { e.GroupID = "other" }, func(e *MessageEvent) { e.ProfileID = "other" }, func(e *MessageEvent) { e.MessageThreadID = "other" }} {
				other := second
				change(&other)
				p.observe(context.Background(), other)
				if got := p.takeReply(other, RandomRepeatConfig{Probability: 100}).text; got != "" {
					t.Fatal("cross scope repeat", got)
				}
			}
			p.observe(context.Background(), second)
			got := p.takeReply(second, RandomRepeatConfig{Probability: probability}).text
			if (got != "") != (roll < probability) {
				t.Fatalf("probability %d roll %d: %q", probability, roll, got)
			}
			if got := p.takeReply(second, RandomRepeatConfig{Probability: 100}).text; got != "" {
				t.Fatal("same message rolled twice")
			}
			second.MessageID = "3"
			second.UserID = "c"
			p.observe(context.Background(), second)
			want := ""
			if roll >= probability {
				want = "安排" // a miss can retry; a successful chain cannot
			}
			if got := p.takeReply(second, RandomRepeatConfig{Probability: 100}).text; got != want {
				t.Fatalf("next message: got %q want %q", got, want)
			}
		}
	}
}

func TestRandomRepeaterRejectsAndBreaksChain(t *testing.T) {
	cases := map[string]func(*MessageEvent){
		"bot":      func(e *MessageEvent) { e.SenderIsBot = true },
		"self":     func(e *MessageEvent) { e.UserID = e.SelfID },
		"mention":  func(e *MessageEvent) { e.ToMe = true },
		"quote":    func(e *MessageEvent) { e.Quoted = &QuotedMessage{} },
		"image":    func(e *MessageEvent) { e.Segments = append(e.Segments, MessageSegment{Type: "image"}) },
		"edit":     func(e *MessageEvent) { e.Edited = true },
		"outbound": func(e *MessageEvent) { e.Outbound = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := newRandomRepeater()
			p.observe(context.Background(), repeatEvent("1", "a", "安排"))
			bad := repeatEvent("2", "b", "安排")
			mutate(&bad)
			p.observe(context.Background(), bad)
			if repeatText(bad) != "" {
				t.Fatal("invalid message accepted")
			}
			next := repeatEvent("3", "c", "安排")
			p.observe(context.Background(), next)
			if p.takeReply(next, RandomRepeatConfig{Probability: 100}).text != "" {
				t.Fatal("invalid message did not break chain")
			}
		})
	}
	for _, text := range []string{"", "  ", "[CQ:at,qq=all]", "/help", "#diana"} {
		if repeatText(repeatEvent("1", "a", text)) != "" {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestRuntimeRandomRepeater(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			manager := NewPluginManager()
			channel := &recordingChannel{}
			r := NewRuntime(BotConfig{RandomRepeat: &RandomRepeatConfig{Enabled: enabled, Threshold: 2, Probability: 100, CooldownSeconds: 60}, ID: "qq", GroupTriggers: []string{"Diana"}, BotAccount: "42"}, channel, manager, nil, nil, nil, nil)
			for i, user := range []string{"a", "b", "c"} {
				if err := r.HandleEvent(context.Background(), repeatEvent(fmt.Sprint(i), user, "安排")); err != nil {
					t.Fatal(err)
				}
			}
			sent := channel.sentSnapshot()
			want := 0
			if enabled {
				want = 1
			}
			if len(sent) != want {
				t.Fatalf("sent %d want %d: %#v", len(sent), want, sent)
			}
			if enabled && (sent[0].Text != "安排" || !sent[0].LiteralText || sent[0].ReplyMessageID != "") {
				t.Fatalf("not a literal repeat: %#v", sent[0])
			}
		})
	}
}

func TestRandomRepeaterPreservesTextAndThreshold(t *testing.T) {
	p := newRandomRepeater()
	text := "  安排\n安排  "
	for i := 0; i < 4; i++ {
		e := repeatEvent(fmt.Sprint(i), fmt.Sprint(i), text)
		p.observe(context.Background(), e)
		got := p.takeReply(e, RandomRepeatConfig{Threshold: 3, Probability: 100}).text
		if i == 2 && got != text {
			t.Fatalf("literal text lost: %q", got)
		}
		if i != 2 && got != "" {
			t.Fatalf("unexpected reply at %d", i)
		}
	}
}

func TestRandomRepeaterTriggersOnceAcrossConcurrentAttempts(t *testing.T) {
	p := newRandomRepeater()
	p.roll = func() int { return 0 }
	p.observe(context.Background(), repeatEvent("1", "a", "安排"))
	event := repeatEvent("2", "b", "安排")
	p.observe(context.Background(), event)
	var replies atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if p.takeReply(event, RandomRepeatConfig{Probability: 100}).text != "" {
				replies.Add(1)
			}
		})
	}
	wg.Wait()
	if replies.Load() != 1 {
		t.Fatalf("concurrent replies=%d, want 1", replies.Load())
	}
	for i := range 5 {
		next := repeatEvent(fmt.Sprint(i+3), fmt.Sprint(i), "安排")
		p.observe(context.Background(), next)
		if got := p.takeReply(next, RandomRepeatConfig{Probability: 100}).text; got != "" {
			t.Fatalf("zero cooldown reopened the chain: %q", got)
		}
	}
}

func TestRuntimeRandomRepeaterOnlyOncePerChain(t *testing.T) {
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 2, Probability: 100, CooldownSeconds: 60}, ID: "qq", GroupTriggers: []string{"Diana"}, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now()
	r.repeater.now = func() time.Time { return now }
	for i, text := range []string{"安排", "安排", "安排", "安排", "好耶", "好耶", "安排", "安排"} {
		if i == 3 || i == 6 {
			now = now.Add(time.Hour)
		}
		if err := r.HandleEvent(context.Background(), repeatEvent(fmt.Sprint(i), fmt.Sprint(i%2), text)); err != nil {
			t.Fatal(err)
		}
	}
	sent := channel.sentSnapshot()
	if len(sent) != 3 || sent[0].Text != "安排" || sent[1].Text != "好耶" || sent[2].Text != "安排" {
		t.Fatalf("unexpected repeat sequence: %#v", sent)
	}
}

func TestRuntimeRandomRepeaterConsumedChainSkipsModel(t *testing.T) {
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 2, Probability: 100}, ID: "qq", BotAccount: "42", GroupTriggers: []string{"Diana"}}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		t.Fatal("consumed repeat reached the model")
		return nil, nil
	})
	ctx := context.Background()
	r.observeRandomRepeat(ctx, repeatEvent("1", "a", "安排"))
	second := repeatEvent("2", "b", "安排")
	r.observeRandomRepeat(ctx, second)
	if outcome := r.tryRandomRepeat(ctx, second); outcome != "replied" {
		t.Fatalf("first repeat outcome=%q", outcome)
	}
	_, _, handled, outcome := r.prepareMessageEvent(ctx, repeatEvent("3", "c", "安排"))
	if handled || outcome != randomRepeatAlreadyTriggeredOutcome || len(channel.sentSnapshot()) != 1 {
		t.Fatalf("consumed chain: handled=%v outcome=%q sent=%d", handled, outcome, len(channel.sentSnapshot()))
	}
	direct := repeatEvent("direct", "c", "Diana，2 加 2 等于多少？")
	direct.ToMe = true
	if _, _, handled, outcome := r.prepareMessageEvent(ctx, direct); !handled {
		t.Fatalf("direct question swallowed: %q", outcome)
	}
	decision, reason, handled := DescribeEventOutcome(randomRepeatAlreadyTriggeredOutcome)
	if decision != "not_replied" || reason == "" || handled {
		t.Fatalf("invalid event description: %q %q %v", decision, reason, handled)
	}
}

func TestRuntimeRandomRepeaterMarkedBotBreaksChain(t *testing.T) {
	manager := NewPluginManager()
	r := NewRuntime(BotConfig{RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 2, Probability: 100, CooldownSeconds: 60}, ID: "qq", BotAccount: "42", MarkedBotIDs: []string{"bot"}}, &recordingChannel{}, manager, nil, nil, nil, nil)
	for i, user := range []string{"a", "bot", "b"} {
		e := repeatEvent(fmt.Sprint(i), user, "安排")
		r.observeRandomRepeat(context.Background(), e)
		if r.repeater.takeReply(e, RandomRepeatConfig{Probability: 100}).text != "" {
			t.Fatal("marked bot counted as human")
		}
	}
}

func TestRuntimeRandomRepeaterDisabledClearsChain(t *testing.T) {
	manager := NewPluginManager()
	r := NewRuntime(BotConfig{RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 2, Probability: 100, CooldownSeconds: 60}, ID: "qq", BotAccount: "42"}, &recordingChannel{}, manager, nil, nil, nil, nil)
	r.observeRandomRepeat(context.Background(), repeatEvent("1", "a", "安排"))
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "qq", BotAccount: "42", RandomRepeat: &RandomRepeatConfig{Enabled: false}}}})
	r.observeRandomRepeat(context.Background(), repeatEvent("2", "b", "其他内容"))
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "qq", BotAccount: "42", RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 2, Probability: 100}}}})
	e := repeatEvent("3", "b", "安排")
	r.observeRandomRepeat(context.Background(), e)
	if r.repeater.takeReply(e, RandomRepeatConfig{Probability: 100}).text != "" {
		t.Fatal("disabled interval kept old chain")
	}
}

type repeaterLiveProvider struct {
	client llm.LLMClient
	calls  atomic.Int32
}

func (p *repeaterLiveProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls.Add(1)
	return p.client.Generate(ctx, req)
}

// Uses the current production model configuration, with all delivery captured locally.
// Replay the requested chain, then check a direct question still reaches the model.
func TestLiveRandomRepeaterReplay(t *testing.T) {
	client := liveLLMClient(t)
	provider := &repeaterLiveProvider{client: client}
	manager := NewPluginManager()
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 2, Probability: 100, CooldownSeconds: 60}, ID: "qq", BotAccount: "42", GroupTriggers: []string{"Diana"}}, channel, manager, nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	now := time.Now()
	r.repeater.now = func() time.Time { return now }
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	for i, user := range []string{"a", "b", "c"} {
		before := provider.calls.Load()
		if err := r.HandleEvent(ctx, repeatEvent(fmt.Sprint(i), user, "安排")); err != nil {
			t.Fatal(err)
		}
		if i == 1 && provider.calls.Load() != before {
			t.Fatal("matching repeat called the model")
		}
	}
	if got := channel.sentSnapshot(); len(got) != 1 || got[0].Text != "安排" {
		t.Fatalf("repeat replay: %#v", got)
	}
	now = now.Add(time.Hour)
	beforeLate := provider.calls.Load()
	if err := r.HandleEvent(ctx, repeatEvent("late", "d", "安排")); err != nil {
		t.Fatal(err)
	}
	if len(channel.sentSnapshot()) != 1 || provider.calls.Load() != beforeLate {
		t.Fatal("same chain triggered again after cooldown")
	}
	beforeDirect := provider.calls.Load()
	e := repeatEvent("direct", "a", "Diana，2 加 2 等于多少？只回答数字。")
	e.ToMe = true
	prepared, text, handled, outcome := r.prepareMessageEvent(ctx, e)
	if !handled {
		t.Fatal("direct question swallowed", outcome)
	}
	if _, err := r.replyAndRecord(ctx, prepared, text, outcome); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() <= beforeDirect || len(channel.sentSnapshot()) < 2 {
		t.Fatal("direct answer missing")
	}
	t.Logf("matching repeat: one literal reply, zero model calls; direct question: %d model calls", provider.calls.Load()-beforeDirect)
}

func TestRuntimeRandomRepeaterGroupOverridesAndAdmission(t *testing.T) {
	for _, mode := range []string{"override", "disabled-group", "disabled-user", "muted"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewPluginManager() // disabled on the robot; enable only this group
			cfg := BotConfig{ID: "qq", BotAccount: "42", GroupTriggers: []string{"Diana"}}
			if mode == "disabled-user" {
				cfg.DisabledUsers = []string{"b"}
			}
			channel := &recordingChannel{}
			r := NewRuntime(cfg, channel, manager, nil, nil, nil, nil)
			group := GroupConfig{GroupID: "123456", BotProfileID: "qq", Enabled: true, EnabledSet: true, InheritanceMigrated: true,
				RandomRepeat: &RandomRepeatConfig{Enabled: true, Probability: 100, Threshold: 3, CooldownSeconds: 60}}
			if mode == "disabled-group" {
				group.Enabled = false
			}
			r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"123456": group}})
			if mode == "muted" {
				notice := botMuteNotice("42", "ban", "600")
				notice.ProfileID, notice.GroupID, notice.Platform = "qq", "123456", PlatformOneBotV11
				r.observeBotMuteNotice(context.Background(), notice)
			}
			for i, user := range []string{"a", "c", "b"} {
				if err := r.HandleEvent(context.Background(), repeatEvent(fmt.Sprint(i), user, "安排")); err != nil {
					t.Fatal(err)
				}
				if i < 2 && len(channel.sentSnapshot()) != 0 {
					t.Fatal("group threshold ignored")
				}
			}
			want := 0
			if mode == "override" {
				want = 1
			}
			if got := len(channel.sentSnapshot()); got != want {
				t.Fatalf("sent=%d want=%d", got, want)
			}
		})
	}
}

func TestRandomRepeatBehaviorConfigRoundTripAndInheritance(t *testing.T) {
	cfg := BotConfig{ID: "qq", RandomRepeat: &RandomRepeatConfig{Enabled: true, Threshold: 3, Probability: 0, CooldownSeconds: 0}}
	payload := PayloadFromConfig(cfg)
	restored := ConfigFromPayload(payload, BotConfig{}).WithDefaults()
	if restored.RandomRepeat == nil || *restored.RandomRepeat != *cfg.RandomRepeat {
		t.Fatalf("lost behavior settings: %#v", restored.RandomRepeat)
	}
	payload.RandomRepeat.Threshold = 9
	if cfg.RandomRepeat.Threshold != 3 || restored.RandomRepeat.Threshold != 3 {
		t.Fatal("configuration pointers alias")
	}
	r := NewRuntime(restored, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	group := GroupConfig{GroupID: "123456", BotProfileID: "qq", Enabled: true, EnabledSet: true, InheritanceMigrated: true}
	store := &stubGroupConfigStore{configs: map[string]GroupConfig{"123456": group}}
	r.SetGroupConfigStore(store)
	event := repeatEvent("1", "a", "安排")
	if got := r.effectiveConfigForEvent(event).RandomRepeat; got == nil || *got != *restored.RandomRepeat {
		t.Fatal("group did not inherit", got)
	}
	group.RandomRepeat = &RandomRepeatConfig{Enabled: false, Threshold: 2, Probability: 0, CooldownSeconds: 0}
	store.configs["123456"] = group
	if got := r.effectiveConfigForEvent(event).RandomRepeat; got.Enabled || got.Probability != 0 || got.CooldownSeconds != 0 {
		t.Fatal("explicit group override lost", got)
	}
	if got := r.ProfileConfig("qq").RandomRepeat; !got.Enabled || got.Threshold != 3 {
		t.Fatal("group override mutated robot", got)
	}
	if _, exists := NewDefaultPluginManager().Get("official.random-repeater"); exists {
		t.Fatal("repeat should not appear in plugins")
	}
}
