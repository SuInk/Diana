// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// QQ 的群头像地址规则只对 OneBot 成立。给 Telegram 群套上去，拿到的既是死链，
// 又会在每次打开控制台时把群号送到腾讯的服务器上。
func TestMergeConsoleGroupItemsSkipsQQAvatarForOtherPlatforms(t *testing.T) {
	base := assistant.BotConfig{}
	set := assistant.GroupConfigSet{}
	live := []botAutoGroupInfo{
		{GroupID: "111", GroupName: "QQ 群", QQAvatar: true},
		{GroupID: "-1001", GroupName: "Telegram 读书会"},
	}
	items := mergeConsoleGroupItems(base, set, live, func(string) bool { return true })
	byID := map[string]consoleGroupItem{}
	for _, item := range items {
		byID[item.GroupID] = item
	}
	if len(byID) != 2 {
		t.Fatalf("items = %#v", items)
	}
	qq := byID["111"]
	if !strings.Contains(qq.AvatarURL, "qlogo.cn") {
		t.Fatalf("onebot group lost its avatar: %#v", qq)
	}
	telegram := byID["-1001"]
	// 关键是不能指向腾讯：那既是死链，也会把 Telegram 群号发给第三方。
	// 现在改走本机的鉴权代理，Bot Token 留在服务端。
	if strings.Contains(telegram.AvatarURL, "qlogo.cn") {
		t.Fatalf("telegram group got a QQ avatar URL: %q", telegram.AvatarURL)
	}
	if !strings.HasPrefix(telegram.AvatarURL, "/api/assistant/groups/") {
		t.Fatalf("telegram group avatar should go through the local proxy: %q", telegram.AvatarURL)
	}
	if telegram.GroupName != "Telegram 读书会" {
		t.Fatalf("telegram group name = %q", telegram.GroupName)
	}
}

// 已保存但当前不在列表里的群，按配置里记的归属机器人判断，不能一律当成 QQ 群。
func TestMergeConsoleGroupItemsUsesProfileForSavedGroups(t *testing.T) {
	set := assistant.GroupConfigSet{}
	set.Groups = []assistant.GroupConfig{
		{GroupID: "111", BotProfileID: "qq-profile"},
		{GroupID: "-1001", BotProfileID: "tg-profile"},
	}
	isOneBot := func(profileID string) bool { return profileID == "qq-profile" }
	items := mergeConsoleGroupItems(assistant.BotConfig{}, set, nil, isOneBot)
	byID := map[string]consoleGroupItem{}
	for _, item := range items {
		byID[item.GroupID] = item
	}
	if got := byID["111"]; !strings.Contains(got.AvatarURL, "qlogo.cn") {
		t.Fatalf("saved onebot group = %#v", got)
	}
	got := byID["-1001"]
	if strings.Contains(got.AvatarURL, "qlogo.cn") {
		t.Fatalf("saved telegram group got a QQ avatar URL: %q", got.AvatarURL)
	}
	// 已保存的群配置带着归属机器人，代理地址要把它一起传下去，
	// 否则多机器人部署下不知道该问哪台机器人要头像。
	if !strings.Contains(got.AvatarURL, "bot_profile_id=tg-profile") {
		t.Fatalf("saved telegram group avatar lost its profile: %q", got.AvatarURL)
	}
}

// 单机器人 Telegram 部署里，入站事件的 profile_id 常常是空的，老数据也可能对不上
// 任何配置档。这种「认不出归属」的情况以前一律按 OneBot 处理，于是给 Telegram 群
// 拼出 QQ 的头像地址，图必然加载失败——用户看到的就是「没有头像」。
func TestIsOneBotProfileDoesNotAssumeOneBotWithoutOneBotProfiles(t *testing.T) {
	telegram := assistant.DefaultBotConfig()
	telegram.ID = "tg-profile"
	telegram.Platform = "telegram"
	store := NewMemoryBotProfileStore(telegram)
	if err := store.SaveProfiles(assistant.ProfileSet{
		ActiveID: "tg-profile",
		Profiles: []assistant.BotConfig{telegram},
	}); err != nil {
		t.Fatal(err)
	}
	handler := &BotHandler{profiles: store}

	if handler.isOneBotProfile("tg-profile") {
		t.Fatal("registered telegram profile must not be treated as OneBot")
	}
	// 认不出归属，但整个部署里没有任何 OneBot 机器人：可以确定不是 OneBot。
	if handler.isOneBotProfile("") {
		t.Fatal("empty profile must not fall back to OneBot when no OneBot bot exists")
	}
	if handler.isOneBotProfile("unknown-profile") {
		t.Fatal("unknown profile must not fall back to OneBot when no OneBot bot exists")
	}
}

// 混合部署里认不出归属时，仍按 OneBot 处理：老部署的事件里 profile_id 本来就是
// 空的，那些群多半就是 QQ 群，保持原来的行为。
func TestIsOneBotProfileKeepsLegacyFallbackWithOneBotProfiles(t *testing.T) {
	onebot := assistant.DefaultBotConfig()
	onebot.ID = "qq-profile"
	onebot.Platform = "onebot-v11"
	telegram := assistant.DefaultBotConfig()
	telegram.ID = "tg-profile"
	telegram.Platform = "telegram"
	store := NewMemoryBotProfileStore(onebot)
	if err := store.SaveProfiles(assistant.ProfileSet{
		ActiveID: "qq-profile",
		Profiles: []assistant.BotConfig{onebot, telegram},
	}); err != nil {
		t.Fatal(err)
	}
	handler := &BotHandler{profiles: store}

	if !handler.isOneBotProfile("qq-profile") {
		t.Fatal("onebot profile must be detected")
	}
	if handler.isOneBotProfile("tg-profile") {
		t.Fatal("telegram profile must not be detected as OneBot")
	}
	if !handler.isOneBotProfile("") {
		t.Fatal("unknown profile should keep the legacy OneBot fallback when a OneBot bot exists")
	}
}
