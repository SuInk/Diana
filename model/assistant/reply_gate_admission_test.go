// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"slices"
	"testing"
)

// TestGroupGateKeepsGlobalUserLists 是这次改动的核心。
//
// 改之前群级门禁是整份替换：在任何一个群填一个屏蔽账号，机器人级的整份门禁在那个
// 群就全归零——全局黑名单、全局豁免、等级门槛、回复时段一起失效。方向是越配越松，
// 而且群设置页只显示本群填的那一条，界面上完全看不出全局那份没了。
//
// 现在门槛仍然整份用群里的（那个「为本群单独设置回复规则」开关就是这个意思），
// 名单走并集。
func TestGroupGateKeepsGlobalUserLists(t *testing.T) {
	base := BotConfig{
		BotAccount: "bot1",
		ReplyGate: &ReplyGate{
			BlockedUsers:       []string{"999"},
			ExemptUsers:        []string{"888"},
			MinGroupLevel:      3,
			ActiveHoursEnabled: true, ActiveStart: "09:00", ActiveEnd: "18:00",
		},
	}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		// 这个群只想额外屏蔽 111
		"g1": {GroupID: "g1", ReplyGate: &ReplyGate{BlockedUsers: []string{"111"}}},
	}})
	gate := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "g1"}).ReplyGate

	if !gate.IsBlocked("999") {
		t.Fatal("全局屏蔽的账号在配过本群门禁的群里不再被拦——全局黑名单被静默丢弃了")
	}
	if !gate.IsBlocked("111") {
		t.Fatal("本群自己填的屏蔽账号没生效")
	}
	if !gate.IsExempt("888") {
		t.Fatal("全局豁免名单被丢弃了")
	}
	// 门槛仍然是群里那份说了算：群里没开时段限制，就不该继承全局的 09:00-18:00。
	if gate.ActiveHoursEnabled || gate.MinGroupLevel != 0 {
		t.Fatalf("门槛不该从全局继承：ActiveHours=%v MinLevel=%d", gate.ActiveHoursEnabled, gate.MinGroupLevel)
	}
}

// TestGroupGateWithoutOwnGateInheritsEverything 没配过本群门禁的群完全跟随全局。
func TestGroupGateWithoutOwnGateInheritsEverything(t *testing.T) {
	base := BotConfig{BotAccount: "bot1", ReplyGate: &ReplyGate{BlockedUsers: []string{"999"}, MinGroupLevel: 3}}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"g1": {GroupID: "g1"}}})
	gate := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "g1"}).ReplyGate
	if !gate.IsBlocked("999") || gate.MinGroupLevel != 3 {
		t.Fatalf("没配本群门禁的群没有完全跟随全局：%#v", gate)
	}
}

func TestUnionStringsDedupesAndKeepsOrder(t *testing.T) {
	got := unionStrings([]string{"111", " 222 ", "111"}, []string{"222", "333", ""})
	if !slices.Equal(got, []string{"111", "222", "333"}) {
		t.Fatalf("unionStrings = %v", got)
	}
	if unionStrings(nil, nil) != nil {
		t.Fatal("两个空名单应该并出 nil，而不是空切片")
	}
}

// TestUserWhitelistOnlyRepliesToListedUsers 白名单是「只回这几个人」。
func TestUserWhitelistOnlyRepliesToListedUsers(t *testing.T) {
	cfg := BotConfig{
		BotAccount: "bot1", OwnerID: "1",
		ReplyGate: &ReplyGate{UserAdmission: UserAdmissionWhitelist, AllowedUsers: []string{"222"}},
	}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	if !runtime.replyGateAllows(cfg, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "222"}) {
		t.Fatal("白名单里的人被拦了")
	}
	if runtime.replyGateAllows(cfg, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "333"}) {
		t.Fatal("白名单外的人被放行了")
	}
	// 主人仍然进得来：配错了自己也被挡在门外是这类开关最常见的事故。
	if !runtime.replyGateAllows(cfg, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}) {
		t.Fatal("主人被自己的白名单挡在门外")
	}
}

// TestExemptUsersCannotBypassWhitelist 豁免不是准入。
//
// 豁免的语义是「绕过等级和时段门槛」。如果白名单判在豁免之后，一个既在豁免名单
// 又不在白名单里的人会被放行——那等于白名单可以被豁免名单绕开。
func TestExemptUsersCannotBypassWhitelist(t *testing.T) {
	cfg := BotConfig{
		BotAccount: "bot1",
		ReplyGate: &ReplyGate{
			UserAdmission: UserAdmissionWhitelist, AllowedUsers: []string{"222"},
			ExemptUsers: []string{"333"},
		},
	}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if runtime.replyGateAllows(cfg, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "333"}) {
		t.Fatal("豁免名单绕开了白名单")
	}
}

// TestBlacklistWinsOverWhitelist 同时在两个名单里时以拦截为准。
func TestBlacklistWinsOverWhitelist(t *testing.T) {
	cfg := BotConfig{
		BotAccount: "bot1",
		ReplyGate: &ReplyGate{
			UserAdmission: UserAdmissionWhitelist, AllowedUsers: []string{"222"},
			BlockedUsers: []string{"222"},
		},
	}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if runtime.replyGateAllows(cfg, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "222"}) {
		t.Fatal("同时在黑白名单里时被放行了，应该以拦截为准")
	}
}

// TestWhitelistIsStickyAcrossLevels 白名单取「任一层开启即开启」。
//
// 这一项越严越安全：机器人级要求只回指定的人，不该被某个群的配置放开。
func TestWhitelistIsStickyAcrossLevels(t *testing.T) {
	base := BotConfig{
		BotAccount: "bot1",
		ReplyGate:  &ReplyGate{UserAdmission: UserAdmissionWhitelist, AllowedUsers: []string{"222"}},
	}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"g1": {GroupID: "g1", ReplyGate: &ReplyGate{BlockedUsers: []string{"111"}}},
	}})
	gate := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "g1"}).ReplyGate
	if !gate.WhitelistEnabled() {
		t.Fatal("群配置把机器人级的白名单模式关掉了")
	}
	if !gate.IsAllowedUser("222") {
		t.Fatal("机器人级的白名单成员没有并进来")
	}
}

// TestBlacklistModeIgnoresAllowedUsers 老配置行为不变：没开白名单就当没有这个名单。
func TestBlacklistModeIgnoresAllowedUsers(t *testing.T) {
	gate := ReplyGate{AllowedUsers: []string{"222"}}.WithDefaults()
	if gate.UserAdmission != UserAdmissionBlacklist {
		t.Fatalf("默认模式 = %q，应该是黑名单", gate.UserAdmission)
	}
	if !gate.IsAllowedUser("333") {
		t.Fatal("黑名单模式下不该按白名单拦人")
	}
}
