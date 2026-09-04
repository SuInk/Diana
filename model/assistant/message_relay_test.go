// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func relayEndpoint(profile, platform, kind, target string) MessageRelayEndpoint {
	return MessageRelayEndpoint{ProfileID: profile, Platform: platform, Kind: kind, TargetID: target}
}

func relayPair(id string, enabled bool, a, b MessageRelayEndpoint) MessageRelayPair {
	return MessageRelayPair{ID: id, Enabled: enabled, Endpoints: []MessageRelayEndpoint{a, b}}
}

// 两端一对：一端进来的消息只该出现在另一端。
func TestMessageRelayForwardsToTheOtherEnd(t *testing.T) {
	pairs := NormalizeMessageRelays([]MessageRelayPair{
		relayPair("p1", true,
			relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "123456"),
			relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "-1001")),
	})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-qq", Platform: PlatformOneBotV11, GroupID: "123456", UserID: "10001"}

	targets := messageRelayTargets(pairs, event)
	if len(targets) != 1 || targets[0].ProfileID != "bot-tg" || targets[0].TargetID != "-1001" {
		t.Fatalf("targets=%+v", targets)
	}

	// 反向同理，链路是双向的。
	back := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-tg", Platform: PlatformTelegram, GroupID: "-1001", UserID: "20002"}
	if targets := messageRelayTargets(pairs, back); len(targets) != 1 || targets[0].TargetID != "123456" {
		t.Fatalf("reverse targets=%+v", targets)
	}
}

// 多对链路叠加：同一个群参与两条链路时，两个对端都要收到。
func TestMessageRelaySupportsMultiplePairs(t *testing.T) {
	hub := relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "123456")
	pairs := NormalizeMessageRelays([]MessageRelayPair{
		relayPair("p1", true, hub, relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "-1001")),
		relayPair("p2", true, hub, relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "654321")),
	})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-qq", Platform: PlatformOneBotV11, GroupID: "123456", UserID: "10001"}

	targets := messageRelayTargets(pairs, event)
	if len(targets) != 2 {
		t.Fatalf("targets=%+v, want both ends of the two pairs", targets)
	}
	got := []string{targets[0].TargetID, targets[1].TargetID}
	if !(got[0] == "-1001" && got[1] == "654321") {
		t.Fatalf("targets=%v", got)
	}
}

// 同一个对端出现在多条链路里时只发一次，否则那边会看到同一条消息刷两遍。
func TestMessageRelayDeduplicatesRepeatedTargets(t *testing.T) {
	source := relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "123456")
	target := relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "-1001")
	pairs := NormalizeMessageRelays([]MessageRelayPair{
		relayPair("p1", true, source, target),
		relayPair("p2", true, source, target),
	})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-qq", Platform: PlatformOneBotV11, GroupID: "123456", UserID: "10001"}
	if targets := messageRelayTargets(pairs, event); len(targets) != 1 {
		t.Fatalf("targets=%+v, want one delivery", targets)
	}
}

// 停用的链路不转发，但配置要留着，方便临时断开再打开。
func TestMessageRelaySkipsDisabledPairs(t *testing.T) {
	pairs := NormalizeMessageRelays([]MessageRelayPair{
		relayPair("p1", false,
			relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "123456"),
			relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "-1001")),
	})
	if len(pairs) != 1 {
		t.Fatalf("disabled pair must stay in the configuration, got %+v", pairs)
	}
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-qq", Platform: PlatformOneBotV11, GroupID: "123456", UserID: "10001"}
	if targets := messageRelayTargets(pairs, event); len(targets) != 0 {
		t.Fatalf("targets=%+v, want nothing forwarded", targets)
	}
}

// 私聊和群聊是两种会话，群号和用户号撞上了也不能混着转。
func TestMessageRelayDistinguishesGroupAndPrivate(t *testing.T) {
	pairs := NormalizeMessageRelays([]MessageRelayPair{
		relayPair("p1", true,
			relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindPrivate, "10001"),
			relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindPrivate, "555")),
	})
	group := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-qq", Platform: PlatformOneBotV11, GroupID: "10001", UserID: "10001"}
	if targets := messageRelayTargets(pairs, group); len(targets) != 0 {
		t.Fatalf("a group message must not match a private endpoint: %+v", targets)
	}
	private := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-qq", Platform: PlatformOneBotV11, UserID: "10001"}
	if targets := messageRelayTargets(pairs, private); len(targets) != 1 {
		t.Fatalf("targets=%+v", targets)
	}
}

// 配不全的链路、两端指向同一个会话的链路都要丢掉：后者会把消息发回原地复读。
func TestNormalizeMessageRelaysDropsUnusablePairs(t *testing.T) {
	same := relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "123456")
	pairs := NormalizeMessageRelays([]MessageRelayPair{
		relayPair("", true, same, same),
		relayPair("", true, same, relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "")),
		{Enabled: true, Endpoints: []MessageRelayEndpoint{same}},
		relayPair("", true, same, relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "-1001")),
	})
	if len(pairs) != 1 {
		t.Fatalf("pairs=%+v, want only the complete pair", pairs)
	}
	if strings.TrimSpace(pairs[0].ID) == "" {
		t.Fatal("a normalized pair must carry an id so the UI can address it")
	}
}

// 机器人被删掉后，牵扯到它的链路不能留着一直往不存在的机器人发。
func TestDeletingProfileDropsItsRelays(t *testing.T) {
	set := ProfileSet{
		ActiveID: "bot-qq",
		Profiles: []BotConfig{{ID: "bot-qq", Platform: PlatformOneBotV11}, {ID: "bot-tg", Platform: PlatformTelegram}},
		MessageRelays: []MessageRelayPair{relayPair("p1", true,
			relayEndpoint("bot-qq", PlatformOneBotV11, MessageRelayKindGroup, "123456"),
			relayEndpoint("bot-tg", PlatformTelegram, MessageRelayKindGroup, "-1001"))},
	}.WithDefaults()
	if len(set.MessageRelays) != 1 {
		t.Fatalf("fixture relays=%+v", set.MessageRelays)
	}
	if next := set.Delete("bot-tg"); len(next.MessageRelays) != 0 {
		t.Fatalf("relays=%+v, want the dangling link removed", next.MessageRelays)
	}
}

// 插件时代的「N 端全互通」要原样搬成两两成对，转发关系不能变。
func TestMigrateMessageRelayPluginSettings(t *testing.T) {
	states := map[string]PluginState{legacyMessageRelayPluginID: {
		Installed: true,
		Enabled:   true,
		Settings: SettingValues{"endpoints": []any{
			map[string]any{"profile_id": "bot-qq", "platform": "onebot-v11", "kind": "group", "target_id": "123456"},
			map[string]any{"profile_id": "bot-tg", "platform": "telegram", "kind": "group", "target_id": "-1001"},
			map[string]any{"profile_id": "bot-qq", "platform": "onebot-v11", "kind": "group", "target_id": "654321"},
		}},
	}}
	set, changed := MigrateMessageRelayPluginSettings(states, ProfileSet{Profiles: []BotConfig{{ID: "bot-qq", Platform: PlatformOneBotV11}}}.WithDefaults())
	if !changed {
		t.Fatal("stored plugin endpoints must be migrated")
	}
	// 三个端点连满是三条链路，转发关系和原来的全互通一致。
	if len(set.MessageRelays) != 3 {
		t.Fatalf("relays=%+v, want the complete graph over three endpoints", set.MessageRelays)
	}
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-qq", Platform: PlatformOneBotV11, GroupID: "123456", UserID: "10001"}
	if targets := messageRelayTargets(set.MessageRelays, event); len(targets) != 2 {
		t.Fatalf("targets=%+v, want the other two endpoints", targets)
	}

	// 已经有配置时不再迁移：用户删掉的链路不该被旧插件配置变回来。
	if _, changed := MigrateMessageRelayPluginSettings(states, set); changed {
		t.Fatal("migration must run only once")
	}
}

// 停用的旧插件迁移过来也应该是停用的。
func TestMigrateMessageRelayKeepsPluginDisabledState(t *testing.T) {
	states := map[string]PluginState{legacyMessageRelayPluginID: {
		Installed: true,
		Settings: SettingValues{"endpoints": []any{
			map[string]any{"profile_id": "bot-qq", "platform": "onebot-v11", "kind": "group", "target_id": "123456"},
			map[string]any{"profile_id": "bot-tg", "platform": "telegram", "kind": "group", "target_id": "-1001"},
		}},
	}}
	set, changed := MigrateMessageRelayPluginSettings(states, ProfileSet{Profiles: []BotConfig{{ID: "bot-qq"}}}.WithDefaults())
	if !changed || len(set.MessageRelays) != 1 {
		t.Fatalf("changed=%v relays=%+v", changed, set.MessageRelays)
	}
	if set.MessageRelays[0].Enabled {
		t.Fatal("a disabled plugin must not come back enabled")
	}
}

// 转发正文要带上来源，否则对面看到的是一句没头没尾的话。
func TestMessageRelayTextCarriesSource(t *testing.T) {
	event := MessageEvent{
		Kind:       EventKindGroup,
		Platform:   PlatformOneBotV11,
		SenderName: "阿白",
		UserID:     "10001",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "晚上吃什么"}}},
	}
	text, images, videos := messageRelayText(event)
	if text != "【QQ · 阿白】\n晚上吃什么" {
		t.Fatalf("text=%q", text)
	}
	if len(images) != 0 || len(videos) != 0 {
		t.Fatalf("images=%v videos=%v", images, videos)
	}
	// 纯图片消息没有正文，也得转过去，只是正文只剩来源标记。
	imageEvent := MessageEvent{Kind: EventKindGroup, Platform: PlatformTelegram, SenderName: "阿白", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.test/a.png"}}}}
	if text, images, _ := messageRelayText(imageEvent); text != "【Telegram · 阿白】" || len(images) != 1 {
		t.Fatalf("text=%q images=%v", text, images)
	}
	// 什么内容都没有就不必转发了。
	if text, _, _ := messageRelayText(MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11}); text != "" {
		t.Fatalf("empty event text=%q", text)
	}
}
