// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 通知对象一旦少了 destination，编辑框就读不出类型：下拉框空白、ID 也跟着空，
// 再保存一次就把真配置覆盖掉。这里锁住响应形状。
func TestReminderDeliveryTargetsForWebCarryDestination(t *testing.T) {
	item := assistant.Reminder{
		NotificationTargetsJSON: `[{"platform":"onebot_v11","profile_id":"p1","group_id":"123456"},{"profile_id":"p1","user_id":"998877"}]`,
	}
	targets := reminderDeliveryTargetsForWeb(item)
	if len(targets) != 2 {
		t.Fatalf("targets=%#v", targets)
	}
	if targets[0].Destination != "group" || targets[0].GroupID != "123456" || targets[0].UserID != "" {
		t.Fatalf("群聊对象映射错误：%#v", targets[0])
	}
	if targets[1].Destination != "private" || targets[1].UserID != "998877" || targets[1].GroupID != "" {
		t.Fatalf("私聊对象映射错误：%#v", targets[1])
	}
	encoded, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0]["destination"] != "group" || decoded[1]["destination"] != "private" {
		t.Fatalf("响应 JSON 缺少 destination：%s", encoded)
	}
}

func TestReminderDeliveryTargetsForWebSkipsEmptyEntries(t *testing.T) {
	if targets := reminderDeliveryTargetsForWeb(assistant.Reminder{}); targets != nil {
		t.Fatalf("没有通知对象时应返回空：%#v", targets)
	}
	item := assistant.Reminder{NotificationTargetsJSON: `[{"profile_id":"p1"},{"group_id":"  "},{"user_id":"42"}]`}
	targets := reminderDeliveryTargetsForWeb(item)
	if len(targets) != 1 || targets[0].Destination != "private" || targets[0].UserID != "42" {
		t.Fatalf("空对象没有被跳过：%#v", targets)
	}
}

func TestRepositoryWatchTargetsAlwaysUseSourceProfileNamespace(t *testing.T) {
	profile := assistant.BotConfig{ID: "tg", Platform: assistant.PlatformTelegram}
	targets := repositoryWatchTargetsFromPayload([]repositoryWatchTargetPayload{
		{Destination: "group", GroupID: "100"},
		{Destination: "private", UserID: "200"},
	}, profile)
	if len(targets) != 2 {
		t.Fatalf("targets=%#v", targets)
	}
	for _, target := range targets {
		if target.ContextNamespace != profile.ID || target.ProfileID != profile.ID || target.Platform != profile.Platform {
			t.Fatalf("target lost its source profile: %#v", target)
		}
	}
}
