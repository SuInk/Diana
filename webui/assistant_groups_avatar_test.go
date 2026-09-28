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

// 没在群管理页配置过的群要带上来源记的归属机器人。订阅通知的群下拉框按
// bot_profile_id 挑群；来源没记的就留空，不拿当前机器人去猜。
func TestMergeConsoleGroupItemsPassesSourceOwnerThrough(t *testing.T) {
	base := assistant.BotConfig{ID: "qq-main"}
	live := []botAutoGroupInfo{
		{GroupID: "10001", GroupName: "来源没记归属"},
		{GroupID: "10002", GroupName: "另一台的群", BotProfileID: "qq-other"},
	}
	items := mergeConsoleGroupItems(base, assistant.GroupConfigSet{}, live, nil, nil)
	byID := map[string]consoleGroupItem{}
	for _, item := range items {
		byID[item.GroupID] = item
	}
	if got := byID["10001"].BotProfileID; got != "" {
		t.Fatalf("来源没记归属却被猜成了当前机器人：%q", got)
	}
	if got := byID["10002"].BotProfileID; got != "qq-other" {
		t.Fatalf("来源记着的归属丢了：%q", got)
	}
}

// 两台机器人同在一个群：全部机器人视图里各是一张卡，各自带自己的配置，不能被
// 前一台吞掉。
func TestMergeConsoleGroupItemsKeepsSameGroupPerBot(t *testing.T) {
	set := assistant.GroupConfigSet{Groups: []assistant.GroupConfig{
		{GroupID: "10001", BotProfileID: "qq-a", Enabled: true, EnabledSet: true},
		{GroupID: "10001", BotProfileID: "qq-b", Enabled: false, EnabledSet: true},
	}}
	live := []botAutoGroupInfo{
		{GroupID: "10001", GroupName: "共享群", BotProfileID: "qq-a"},
		{GroupID: "10001", GroupName: "共享群", BotProfileID: "qq-b"},
	}
	items := mergeConsoleGroupItems(assistant.BotConfig{}, set, live, nil, nil)
	if len(items) != 2 {
		t.Fatalf("同一个群在两台机器人下应各列一张：%#v", items)
	}
	enabled := map[string]bool{}
	for _, item := range items {
		if !item.Configured || !item.Joined {
			t.Fatalf("配置没对上：%#v", item)
		}
		enabled[item.BotProfileID] = item.Enabled
	}
	if !enabled["qq-a"] || enabled["qq-b"] {
		t.Fatalf("两台机器人的配置串了：%#v", enabled)
	}
}

// 老部署的群配置没写归属，实时列表记了归属时仍要认得出来，不能变成两张卡。
func TestMergeConsoleGroupItemsMatchesLegacyConfigWithoutOwner(t *testing.T) {
	set := assistant.GroupConfigSet{Groups: []assistant.GroupConfig{{GroupID: "10001"}}}
	live := []botAutoGroupInfo{{GroupID: "10001", BotProfileID: "qq-a"}}
	items := mergeConsoleGroupItems(assistant.BotConfig{}, set, live, nil, nil)
	if len(items) != 1 || !items[0].Configured || !items[0].Joined {
		t.Fatalf("老配置没和实时列表合上：%#v", items)
	}
}
