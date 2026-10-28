// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// crossPlatformFixture 是一个实例里同时跑 QQ 和 Telegram 两台机器人的运行时。
func crossPlatformFixture(t *testing.T) (*Runtime, *moderationTestChannel, *moderationTestChannel) {
	t.Helper()
	qq := newModerationTestChannel("owner")
	tg := newModerationTestChannel("administrator")
	multi := NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: qq},
		{ProfileID: "tg", Platform: PlatformTelegram, Channel: tg},
	})
	qqCfg := BotConfig{ID: "qq", Name: "QQ 号", Platform: PlatformOneBotV11, Enabled: true, OwnerID: "owner", BotAccount: "10000"}
	tgCfg := BotConfig{ID: "tg", Name: "TG 号", Platform: PlatformTelegram, Enabled: true, OwnerID: "77777", BotAccount: "20000", TelegramBotToken: "t"}
	r := NewRuntime(qqCfg, multi, NewDefaultPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{qqCfg, tgCfg}})
	return r, qq, tg
}

func qqOwnerPrivateEvent() MessageEvent {
	return MessageEvent{Kind: EventKindPrivate, MessageType: "private", ProfileID: "qq", ContextNamespace: "qq", Platform: PlatformOneBotV11, UserID: "owner", SelfID: "10000"}
}

func TestCrossPlatformToolIsOwnerOnly(t *testing.T) {
	r, _, tg := crossPlatformFixture(t)
	member := qqOwnerPrivateEvent()
	member.UserID = "55555"
	tool := newDianaCrossPlatformTool(r, member)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "bots"}); err == nil || !strings.Contains(err.Error(), "只有机器人主人") {
		t.Fatalf("member bots error = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "send", "profile_id": "tg", "group_id": "-1001", "message": "hi"}); err == nil {
		t.Fatal("member send must be refused")
	}
	if sent := tg.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("member reached telegram: %#v", sent)
	}
}

func TestCrossPlatformToolListsBotsAndSendsToOtherPlatform(t *testing.T) {
	r, _, tg := crossPlatformFixture(t)
	tool := newDianaCrossPlatformTool(r, qqOwnerPrivateEvent())

	output, err := tool.Run(context.Background(), map[string]any{"operation": "bots"})
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Bots []dianaCrossPlatformBot `json:"bots"`
	}
	if err := json.Unmarshal([]byte(output), &listing); err != nil || len(listing.Bots) != 2 {
		t.Fatalf("bots output=%s err=%v", output, err)
	}
	if listing.Bots[0].ProfileID != "qq" || !listing.Bots[0].Current || listing.Bots[1].Platform != PlatformTelegram {
		t.Fatalf("bots = %#v", listing.Bots)
	}

	output, err = tool.Run(context.Background(), map[string]any{"operation": "send", "profile_id": "tg", "group_id": "-1001", "message": "来自 QQ 的通知"})
	if err != nil || !strings.Contains(output, `"delivered":true`) || !strings.Contains(output, "telegram 群 -1001") {
		t.Fatalf("send output=%s err=%v", output, err)
	}
	sent := tg.sentSnapshot()
	if len(sent) != 1 || sent[0].GroupID != "-1001" || !strings.Contains(sent[0].Text, "来自 QQ 的通知") || sent[0].ProfileID != "tg" {
		t.Fatalf("telegram sent = %#v", sent)
	}

	if _, err := tool.Run(context.Background(), map[string]any{"operation": "send", "profile_id": "nope", "group_id": "1", "message": "x"}); err == nil || !strings.Contains(err.Error(), "没有 ID 为 nope") {
		t.Fatalf("unknown profile error = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "send", "profile_id": "tg", "group_id": "某个群", "message": "x"}); err == nil || !strings.Contains(err.Error(), "不能使用名称") {
		t.Fatalf("group name target error = %v", err)
	}
}

func TestCrossPlatformToolReadsOtherBotHistory(t *testing.T) {
	r, _, _ := crossPlatformFixture(t)
	tgGroup := MessageEvent{Kind: EventKindGroup, ProfileID: "tg", ContextNamespace: "tg", Platform: PlatformTelegram, GroupID: "-1001"}
	r.mu.Lock()
	r.history[sessionKey(tgGroup)] = []MessageEvent{
		{Kind: EventKindGroup, ProfileID: "tg", ContextNamespace: "tg", Platform: PlatformTelegram, GroupID: "-1001", UserID: "31415", SenderName: "Ann", MessageID: "m1", RawMessage: "周末去爬山", Time: 1700000000},
	}
	r.mu.Unlock()
	tool := newDianaCrossPlatformTool(r, qqOwnerPrivateEvent())
	output, err := tool.Run(context.Background(), map[string]any{"operation": "recent", "profile_id": "tg", "group_id": "-1001"})
	if err != nil || !strings.Contains(output, "周末去爬山") || !strings.Contains(output, "不是当前会话") {
		t.Fatalf("recent output=%s err=%v", output, err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "recent", "profile_id": "tg"}); err == nil || !strings.Contains(err.Error(), "group_id 或 user_id") {
		t.Fatalf("recent without target error = %v", err)
	}
}

// 主人在 QQ 私聊里让机器人去 Telegram 群里禁言；门禁照常走目标平台那一套。
func TestPlatformToolCrossProfileModeration(t *testing.T) {
	r, qq, tg := crossPlatformFixture(t)
	owner := newDianaPlatformTool(context.Background(), r, qqOwnerPrivateEvent())
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "mute", "profile_id": "tg", "group_id": "-1001", "user_id": "31415", "duration": 60}); err != nil {
		t.Fatal(err)
	}
	if got := recordedCallsByAction(tg.callsSnapshot(), "restrictChatMember"); len(got) != 1 || got[0].params["chat_id"] != "-1001" {
		t.Fatalf("telegram calls = %#v", tg.callsSnapshot())
	}
	if len(qq.callsSnapshot()) != 0 {
		t.Fatalf("qq channel must not be used: %#v", qq.callsSnapshot())
	}
	// 目标平台不支持的操作照样如实拒绝。
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "set_card", "profile_id": "tg", "group_id": "-1001", "user_id": "31415", "card": "x"}); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("telegram set_card error = %v", err)
	}
	// 不能对目标机器人的主人下手。
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "kick", "profile_id": "tg", "group_id": "-1001", "user_id": "77777"}); err == nil || !strings.Contains(err.Error(), "主人") {
		t.Fatalf("kick target owner error = %v", err)
	}
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "recall", "profile_id": "tg"}); err == nil || !strings.Contains(err.Error(), "不能跨机器人") {
		t.Fatalf("cross recall error = %v", err)
	}

	// 群管理员的权力只在自己的群里，不能借 profile_id 伸到别的平台。
	admin := qqOwnerPrivateEvent()
	admin.Kind, admin.GroupID, admin.UserID = EventKindGroup, "123", "admin1"
	adminTool := newDianaPlatformTool(context.Background(), r, admin)
	if _, err := adminTool.Run(context.Background(), map[string]any{"operation": "mute", "profile_id": "tg", "group_id": "-1001", "user_id": "31415", "duration": 60}); err == nil || !strings.Contains(err.Error(), "只有机器人主人") {
		t.Fatalf("admin cross-profile error = %v", err)
	}
	if got := recordedCallsByAction(tg.callsSnapshot(), "restrictChatMember"); len(got) != 1 {
		t.Fatalf("admin reached telegram: %#v", got)
	}
}

// 跨机器人发私聊时，这边的群不是那边的共同群，不能借来开临时会话。
func TestResolvePrivateDeliveryOnlyBorrowsSameProfileGroup(t *testing.T) {
	r, _, _ := crossPlatformFixture(t)
	source := MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "123"}
	target := MessageEvent{Kind: EventKindPrivate, ProfileID: "tg", Platform: PlatformTelegram, UserID: "31415"}
	delivered, _ := r.resolvePrivateDelivery(context.Background(), source, target)
	if delivered.tempSessionGroupID != "" {
		t.Fatalf("borrowed cross-profile group %q", delivered.tempSessionGroupID)
	}
}
