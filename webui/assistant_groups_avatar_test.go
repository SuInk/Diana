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
	if telegram.AvatarURL != "" {
		t.Fatalf("telegram group got a QQ avatar URL: %q", telegram.AvatarURL)
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
	if got := byID["-1001"]; got.AvatarURL != "" {
		t.Fatalf("saved telegram group got a QQ avatar URL: %q", got.AvatarURL)
	}
}
