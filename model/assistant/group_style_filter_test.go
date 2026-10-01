// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type styleFilterGroupConfigs map[string]GroupConfig

func (s styleFilterGroupConfigs) ConfigForGroup(_ string, groupID string) (GroupConfig, bool) {
	cfg, ok := s[groupID]
	return cfg, ok
}

func TestStyleFilterDefaultsOnAndFollowsGroupOverride(t *testing.T) {
	cfg := BotConfig{ID: "bot"}.WithDefaults()
	if !styleFilterEnabled(cfg) {
		t.Fatal("style filter must default to on")
	}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(styleFilterGroupConfigs{
		"off":    {GroupID: "off", StyleFilterEnabled: boolPointer(false)},
		"follow": {GroupID: "follow"},
	})
	if styleFilterEnabled(runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "off"})) {
		t.Fatal("a group that turned the filter off must not filter")
	}
	if !styleFilterEnabled(runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "follow"})) {
		t.Fatal("a group without an override must follow the bot")
	}
}

// 写风格笔记时，过滤开着就把「不记怪话」接在学习提示词后面；本群关掉就不接。
func TestStyleFilterJoinsGroupStyleLearningPrompt(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"这个群爱说「绷不住了」", "这个群爱说「绷不住了」"}}
	runtime, _, event := groupStyleTestRuntime(t, groupStyleMinMessages, provider)
	if _, err := runtime.learnGroupStyle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if system := provider.requests[0].Messages[0].Content; !strings.Contains(system, promptGroupStyleLearnFilter) {
		t.Fatalf("learning prompt is missing the filter: %q", system)
	}

	runtime.SetGroupConfigStore(styleFilterGroupConfigs{"g1": {GroupID: "g1", StyleFilterEnabled: boolPointer(false)}})
	if _, err := runtime.learnGroupStyle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if system := provider.requests[1].Messages[0].Content; strings.Contains(system, promptGroupStyleLearnFilter) {
		t.Fatalf("a group with the filter off must learn as before: %q", system)
	}
}

// 回复时，注入了「学群友的腔调」就跟一段「这几类不学」，只跟一次；关掉、私聊、
// 没在学群友时都不加。
func TestStyleFilterFollowsGroupVoiceInReplyTail(t *testing.T) {
	cfg := BotConfig{ID: "bot", BotAccount: "42"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "g1", UserID: "10001", SelfID: "42"}
	tail := func(event MessageEvent) string {
		relationship := RelationshipPolicyForConfig(runtime.effectiveConfigForEvent(event), UserMemoryProfile{}, event.UserID)
		_, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)
		return tail
	}
	if strings.Contains(tail(event), promptStyleFilter) {
		t.Fatal("no group voice yet, so there is nothing to filter")
	}
	for i := 0; i < 20; i++ {
		item := event
		item.MessageID = fmt.Sprint(i)
		item.UserID = fmt.Sprint(10001 + i%3)
		item.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天吃什么好呢"}}}
		runtime.remember(item)
	}
	got := tail(event)
	voice := strings.Index(got, "学的是上面群友的消息")
	filter := strings.Index(got, promptStyleFilter)
	if voice < 0 || filter < voice || strings.Count(got, promptStyleFilter) != 1 {
		t.Fatalf("filter must follow the group voice exactly once: %q", got)
	}

	runtime.SetGroupConfigStore(styleFilterGroupConfigs{"g1": {GroupID: "g1", StyleFilterEnabled: boolPointer(false)}})
	if got := tail(event); strings.Contains(got, promptStyleFilter) || !strings.Contains(got, "学的是上面群友的消息") {
		t.Fatalf("a group with the filter off keeps the voice line but no filter: %q", got)
	}

	if styleFilterPrompt(cfg) != "" || styleFilterPrompt(cfg, "", "") != "" {
		t.Fatal("nothing learned from the group, nothing to filter")
	}
}

func TestStyleFilterRulesAreCleanedAndCapped(t *testing.T) {
	long := strings.Repeat("长", styleFilterRuleMaxRunes+20)
	raw := "- 别学「典」\n\n  * 别叫人老婆 \n别学「典」\n" + long + "\n" + long + "x"
	for i := 0; i < styleFilterMaxRules+5; i++ {
		raw += fmt.Sprintf("\n规则%d", i)
	}
	rules := styleFilterRules(raw)
	if len(rules) != styleFilterMaxRules || rules[0] != "别学「典」" || rules[1] != "别叫人老婆" {
		t.Fatalf("rules = %q", rules)
	}
	if got := []rune(rules[2]); len(got) != styleFilterRuleMaxRunes || rules[3] == rules[2] {
		t.Fatalf("long rules must be cut and deduped after cutting: %q", rules[2:4])
	}
}

// 群里另加的规则和机器人的合并生效；两处注入都带上，过滤关掉时连自定义规则一起不带。
func TestStyleFilterCustomRulesMergeAndInject(t *testing.T) {
	cfg := BotConfig{ID: "bot", BotAccount: "42", StyleFilterRules: "别学「典」"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(styleFilterGroupConfigs{
		"g1":  {GroupID: "g1", StyleFilterRules: "别学「典」\n别叫人老婆"},
		"off": {GroupID: "off", StyleFilterEnabled: boolPointer(false), StyleFilterRules: "别叫人老婆"},
	})
	merged := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "g1"})
	if merged.StyleFilterRules != "别学「典」\n别叫人老婆" {
		t.Fatalf("merged rules = %q", merged.StyleFilterRules)
	}
	if other := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "g2"}); other.StyleFilterRules != "别学「典」" {
		t.Fatalf("a group without its own rules uses the bot's: %q", other.StyleFilterRules)
	}

	reply := styleFilterPrompt(merged, "学群友")
	if !strings.Contains(reply, promptStyleFilter) || !strings.Contains(reply, "另外这几条也不学：\n- 别学「典」\n- 别叫人老婆") {
		t.Fatalf("reply filter = %q", reply)
	}
	if learn := styleFilterLearnPrompt(merged); !strings.Contains(learn, promptGroupStyleLearnFilter) || !strings.Contains(learn, "也不要写进笔记：\n- 别学「典」\n- 别叫人老婆") {
		t.Fatalf("learn filter = %q", learn)
	}

	off := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "off"})
	if styleFilterPrompt(off, "学群友") != "" || styleFilterLearnPrompt(off) != "" {
		t.Fatal("custom rules must not apply when the filter is off")
	}
	plain := BotConfig{ID: "bot"}.WithDefaults()
	if got := styleFilterPrompt(plain, "学群友"); got != promptStyleFilter {
		t.Fatalf("no custom rules, only the built-in part: %q", got)
	}
}
