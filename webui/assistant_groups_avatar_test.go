// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 所有平台的群头像都走本机端点，界面上不出现任何第三方地址：Telegram 的地址里带
// Bot Token，QQ 的地址会把群号送到腾讯，而且腾讯的 CDN 回 30 天强缓存，换了头像
// 界面上一个月都不变。地址由后端按内容哈希给出，前端不拼。
func TestMergeConsoleGroupItemsRoutesEveryPlatformThroughLocalAvatars(t *testing.T) {
	base := assistant.BotConfig{}
	set := assistant.GroupConfigSet{}
	live := []botAutoGroupInfo{
		{GroupID: "111", GroupName: "QQ 群", QQAvatar: true},
		{GroupID: "-1001", GroupName: "Telegram 读书会"},
	}
	items := mergeConsoleGroupItems(base, set, live, func(profileID, groupID string) string {
		return "/api/assistant/avatars/group/" + groupID
	}, nil)
	byID := map[string]consoleGroupItem{}
	for _, item := range items {
		byID[item.GroupID] = item
	}
	if len(byID) != 2 {
		t.Fatalf("items = %#v", items)
	}
	for _, item := range items {
		if strings.Contains(item.AvatarURL, "qlogo.cn") {
			t.Fatalf("头像地址直连了第三方：%q", item.AvatarURL)
		}
		if !strings.HasPrefix(item.AvatarURL, "/api/assistant/avatars/") {
			t.Fatalf("头像没走本机端点：%q", item.AvatarURL)
		}
	}
	telegram := byID["-1001"]
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
	items := mergeConsoleGroupItems(assistant.BotConfig{}, set, nil, func(profileID, groupID string) string {
		return "/api/assistant/avatars/group/" + groupID + "?bot_profile_id=" + profileID
	}, nil)
	byID := map[string]consoleGroupItem{}
	for _, item := range items {
		byID[item.GroupID] = item
	}
	if got := byID["111"]; !strings.Contains(got.AvatarURL, "bot_profile_id=qq-profile") {
		t.Fatalf("saved onebot group lost its profile: %#v", got)
	}
	got := byID["-1001"]
	// 已保存的群配置带着归属机器人，头像地址要把它一起传下去，
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
