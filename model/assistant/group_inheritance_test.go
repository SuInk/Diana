// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"slices"
	"testing"
)

// 群配置里留空的项要跟随机器人「现在」的值。以前新建群配置会把机器人当时的触发词、
// 欢迎开关、上下文预算等抄进来落盘，之后机器人页怎么改都进不了这个群。
func TestEmptyGroupSettingsFollowLaterBotChanges(t *testing.T) {
	bot := BotConfig{ID: "bot", GroupTriggers: []string{"旧名"}, MaxReplyChars: 800}.WithDefaults()
	group := DefaultGroupConfig("1", bot)
	group.BotProfileID = "bot"
	stored := group.WithDefaults("1", bot)
	if len(stored.GroupTriggers) != 0 || stored.MaxReplyChars != 0 || stored.WelcomeEnabled != nil || stored.RecallReplyAutoDeleteEnabled != nil {
		t.Fatalf("new group config copied bot values: %+v", stored)
	}

	// 机器人页后来改了。
	bot.GroupTriggers = []string{"新名"}
	bot.MaxReplyChars = 1200
	bot.WelcomeEnabled = true
	bot.RecallReplyAutoDeleteEnabled = boolPointer(true)
	bot.RecallReplyTTLSeconds = 45
	runtime := NewRuntime(bot, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"1": stored}})
	cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "1"})
	if !slices.Equal(cfg.GroupTriggers, []string{"新名"}) || cfg.MaxReplyChars != 1200 || !cfg.WelcomeEnabled ||
		!boolValue(cfg.RecallReplyAutoDeleteEnabled, false) || cfg.RecallReplyTTLSeconds != 45 {
		t.Fatalf("group did not follow the bot: triggers=%v max=%d welcome=%v recall=%v/%d",
			cfg.GroupTriggers, cfg.MaxReplyChars, cfg.WelcomeEnabled, cfg.RecallReplyAutoDeleteEnabled, cfg.RecallReplyTTLSeconds)
	}
}

// 群里真填了的值仍然覆盖机器人，包括开关类的「关」。
func TestExplicitGroupSettingsStillOverride(t *testing.T) {
	bot := BotConfig{ID: "bot", GroupTriggers: []string{"机器人"}, WelcomeEnabled: true, MaxReplyChars: 800}.WithDefaults()
	group := GroupConfig{
		GroupID: "1", BotProfileID: "bot", InheritanceMigrated: true,
		GroupTriggers: []string{"本群名"}, WelcomeEnabled: boolPointer(false), MaxReplyChars: 300,
	}
	runtime := NewRuntime(bot, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"1": group}})
	cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "1"})
	if !slices.Equal(cfg.GroupTriggers, []string{"本群名"}) || cfg.WelcomeEnabled || cfg.MaxReplyChars != 300 {
		t.Fatalf("explicit group values lost: triggers=%v welcome=%v max=%d", cfg.GroupTriggers, cfg.WelcomeEnabled, cfg.MaxReplyChars)
	}
}

// 旧数据的迁移：和所属机器人现在的值相同的快照清成跟随（这一刻行为不变），不同的
// 保留。旧版欢迎开关的 false 不落盘，读回来的 nil 按关闭处理，机器人开着欢迎时不能
// 让这批群升级后突然开始欢迎。
func TestLegacyGroupSnapshotsAreClearedOnlyWhenEqualToBot(t *testing.T) {
	bot := BotConfig{ID: "bot", GroupTriggers: []string{"Diana"}, WelcomeEnabled: true, MaxReplyChars: 800, RecentContextLimit: 30}.WithDefaults()
	var legacy GroupConfig
	raw := `{"bot_profile_id":"bot","group_id":"1","enabled":true,"group_triggers":["Diana"],"max_reply_chars":800,"recent_context_limit":12}`
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		t.Fatal(err)
	}
	migrated := legacy.WithDefaults("1", bot)
	if !migrated.InheritanceMigrated {
		t.Fatal("migration marker not set")
	}
	if len(migrated.GroupTriggers) != 0 || migrated.MaxReplyChars != 0 {
		t.Fatalf("snapshots equal to the bot must follow it: triggers=%v max=%d", migrated.GroupTriggers, migrated.MaxReplyChars)
	}
	if migrated.RecentContextLimit != 12 {
		t.Fatalf("a value different from the bot is a real override, got %d", migrated.RecentContextLimit)
	}
	if migrated.WelcomeEnabled == nil || *migrated.WelcomeEnabled {
		t.Fatalf("legacy group without welcome must stay off while the bot welcomes, got %v", migrated.WelcomeEnabled)
	}

	// 只迁一次：迁完之后群里再填一个和机器人相同的值，是用户自己的选择，不能再被清。
	migrated.MaxReplyChars = 800
	if got := migrated.WithDefaults("1", bot).MaxReplyChars; got != 800 {
		t.Fatalf("migration ran twice, max=%d", got)
	}

	// 拿别的机器人归一化时不迁：拿错了 base，会把真的单独设置当快照清掉。
	other := BotConfig{ID: "other", RecentContextLimit: 12}.WithDefaults()
	if got := legacy.WithDefaults("1", other); got.InheritanceMigrated || got.RecentContextLimit != 12 {
		t.Fatalf("migrated against the wrong bot: %+v", got)
	}
}
