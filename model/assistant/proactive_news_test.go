// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestProactiveGroupAllowedFollowsBotUnlessGroupOverrides(t *testing.T) {
	on, off := true, false
	master := BotConfig{ProactiveMessaging: &ProactiveMessaging{Enabled: true}}
	idle := BotConfig{}
	legacy := BotConfig{ProactiveMessaging: &ProactiveMessaging{Enabled: true, GroupIDs: []string{"10001"}}}
	cases := []struct {
		name     string
		bot      BotConfig
		override *bool
		group    string
		want     bool
	}{
		{"机器人开了，所有群跟随", master, nil, "10002", true},
		{"本群单独关掉", master, &off, "10002", false},
		{"机器人没开，本群单独打开", idle, &on, "10002", true},
		{"机器人没开也没单独开", idle, nil, "10002", false},
		{"旧版名单里的群跟随", legacy, nil, "10001", true},
		{"旧版名单外的群不跟随", legacy, nil, "10002", false},
		{"旧版名单外的群单独打开", legacy, &on, "10002", true},
	}
	for _, tc := range cases {
		groupCfg := tc.bot
		groupCfg.groupProactiveMessagingOverride = tc.override
		if got := proactiveGroupAllowed(tc.bot, groupCfg, proactiveSettings(tc.bot), tc.group); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 机器人一开，最近在聊的群自动成为候选，不用填群号；两天没人说话的群不算。
func TestProactiveTargetsCoverRecentlyActiveGroups(t *testing.T) {
	runtime := newLLMWelcomeRuntime(BotConfig{
		ID: "bot-a", Enabled: true, Platform: "onebot-v11",
		ReplySafetyMasterEnabled: boolPointer(false),
		ProactiveMessaging:       &ProactiveMessaging{Enabled: true},
	}, &recordingChannel{}, &welcomeStubLLM{text: "随便"})
	now := time.Now()
	rememberWarmGroup(runtime, now)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "10002", UserID: "20009", MessageID: "old", RawMessage: "早",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "早"}}}, Time: now.Add(-72 * time.Hour).Unix()})

	configs := runtime.proactiveEnabledConfigs()
	if len(configs) != 1 {
		t.Fatalf("enabled configs = %d", len(configs))
	}
	var groups []string
	for _, target := range runtime.proactiveTargets(configs[0]) {
		groups = append(groups, target.event.GroupID)
	}
	if strings.Join(groups, ",") != "10001" {
		t.Fatalf("targets = %v", groups)
	}
}

func TestParseProactiveNewsQuery(t *testing.T) {
	for raw, want := range map[string]string{
		"```json\n{\"query\":\"  原神  新版本 \"}\n```": "原神 新版本",
		`{"query":""}`: "",
		"没有合适的话题":      "",
	} {
		if got := parseProactiveNewsQuery(raw); got != want {
			t.Errorf("parse(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestProactiveNewsChatLinesSkipsBotAndKeepsOrder(t *testing.T) {
	now := time.Now()
	text := func(s string) []MessageSegment {
		return []MessageSegment{{Type: "text", Data: map[string]string{"text": s}}}
	}
	history := []MessageEvent{
		{Kind: EventKindGroup, UserID: "20001", SenderName: "小明", Segments: text("新赛季开了"), Time: now.Add(-3 * time.Minute).Unix()},
		{Kind: EventKindGroup, UserID: "30001", Outbound: true, Segments: text("我也想玩"), Time: now.Add(-2 * time.Minute).Unix()},
		{Kind: EventKindGroup, UserID: "20002", Segments: text("排位好难"), Time: now.Add(-time.Minute).Unix()},
	}
	got := proactiveNewsChatLines(history, "30001")
	if strings.Join(got, "|") != "小明：新赛季开了|20002：排位好难" {
		t.Fatalf("lines = %#v", got)
	}
}

func TestProactiveRequestTextCarriesNewsMaterial(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001"}
	plan := proactivePlan{IdleFor: 3 * time.Hour}
	plain := proactiveRequestText(BotConfig{}, event, plan, proactiveNews{Query: "原神", Results: ""})
	if strings.Contains(plain, "新鲜事") {
		t.Fatalf("empty results must not add material:\n%s", plain)
	}
	withNews := proactiveRequestText(BotConfig{}, event, plan, proactiveNews{Query: "原神", Results: "原神 6.2 版本前瞻定档"})
	for _, want := range []string{"联网搜了「原神」", "原神 6.2 版本前瞻定档", "不可信的外部文本", "[SKIP]"} {
		if !strings.Contains(withNews, want) {
			t.Fatalf("request missing %q:\n%s", want, withNews)
		}
	}
}

func TestProactiveSearchNewsDefaultsOn(t *testing.T) {
	off := false
	if !proactiveSearchNewsEnabled(&ProactiveMessaging{}) || proactiveSearchNewsEnabled(&ProactiveMessaging{SearchNews: &off}) {
		t.Fatal("search news should default on and honour an explicit off")
	}
}
