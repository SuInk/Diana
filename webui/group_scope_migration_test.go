// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func migrationStores(t *testing.T, profiles ...assistant.BotConfig) (*MemoryBotProfileStore, *MemoryBotGroupConfigStore) {
	t.Helper()
	profileStore := NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: profiles})
	groupStore := NewMemoryBotGroupConfigStore()
	groupStore.SetProfileSource(profileStore)
	return profileStore, groupStore
}

func groupEnabled(t *testing.T, store *MemoryBotGroupConfigStore, profileID, groupID string) (bool, bool) {
	t.Helper()
	cfg, ok := store.ConfigForGroup(profileID, groupID)
	if !ok {
		return false, false
	}
	return cfg.Enabled, true
}

// 白名单模式迁完必须保持原样：名单里的群继续工作，名单外的已知群被显式关掉，
// 没有群配置的群交给「新群默认关」接着挡。
func TestMigrateWhitelistIntoGroupSwitches(t *testing.T) {
	profileStore, groupStore := migrationStores(t, assistant.BotConfig{
		ID: "a", BotAccount: "42",
		GroupAdmission: assistant.GroupAdmission{
			Mode:          assistant.GroupAdmissionWhitelist,
			AllowedGroups: []string{"100"},
		},
	})
	// 名单外但已经有群配置、而且是开着的群：迁移后必须关掉，否则它会突然开口。
	if _, err := groupStore.SaveGroupConfig(assistant.GroupConfig{
		BotProfileID: "a", GroupID: "200", Enabled: true, EnabledSet: true,
	}, profileStore.Profiles().Profiles[0]); err != nil {
		t.Fatal(err)
	}

	if err := MigrateGroupScopeSwitches(profileStore, groupStore); err != nil {
		t.Fatal(err)
	}

	if enabled, ok := groupEnabled(t, groupStore, "a", "100"); !ok || !enabled {
		t.Fatalf("白名单里的群应当开着：enabled=%v configured=%v", enabled, ok)
	}
	if enabled, ok := groupEnabled(t, groupStore, "a", "200"); !ok || enabled {
		t.Fatalf("白名单外的群应当被关掉：enabled=%v configured=%v", enabled, ok)
	}
	profile := profileStore.Profiles().Profiles[0]
	if len(profile.GroupAdmission.AllowedGroups) != 0 {
		t.Fatalf("白名单没有清空：%v", profile.GroupAdmission.AllowedGroups)
	}
	if profile.GroupAdmission.Mode != assistant.GroupAdmissionWhitelist {
		t.Fatalf("新群默认丢了：%q", profile.GroupAdmission.Mode)
	}
}

// 聊天指令停过的群迁进群配置，其余已配置的群保持自己的开关。
func TestMigrateDisabledGroupsIntoGroupSwitches(t *testing.T) {
	profileStore, groupStore := migrationStores(t, assistant.BotConfig{
		ID: "a", BotAccount: "42", DisabledGroups: []string{"300"},
	})
	if _, err := groupStore.SaveGroupConfig(assistant.GroupConfig{
		BotProfileID: "a", GroupID: "400", Enabled: true, EnabledSet: true,
	}, profileStore.Profiles().Profiles[0]); err != nil {
		t.Fatal(err)
	}

	if err := MigrateGroupScopeSwitches(profileStore, groupStore); err != nil {
		t.Fatal(err)
	}

	if enabled, ok := groupEnabled(t, groupStore, "a", "300"); !ok || enabled {
		t.Fatalf("聊天里停用的群应当关着：enabled=%v configured=%v", enabled, ok)
	}
	if enabled, ok := groupEnabled(t, groupStore, "a", "400"); !ok || !enabled {
		t.Fatalf("没被停用的群不该被动过：enabled=%v configured=%v", enabled, ok)
	}
	if groups := profileStore.Profiles().Profiles[0].DisabledGroups; len(groups) != 0 {
		t.Fatalf("旧禁用群名单没有清空：%v", groups)
	}
}

// 迁移跑第二遍什么都不该改：两份名单已经空了，直接走空转。
func TestMigrateIsIdempotent(t *testing.T) {
	profileStore, groupStore := migrationStores(t, assistant.BotConfig{
		ID: "a", BotAccount: "42", DisabledGroups: []string{"300"},
		GroupAdmission: assistant.GroupAdmission{Mode: assistant.GroupAdmissionWhitelist, AllowedGroups: []string{"100"}},
	})
	if err := MigrateGroupScopeSwitches(profileStore, groupStore); err != nil {
		t.Fatal(err)
	}
	before := groupStore.Groups()

	if err := MigrateGroupScopeSwitches(profileStore, groupStore); err != nil {
		t.Fatal(err)
	}

	if after := groupStore.Groups(); len(after.Groups) != len(before.Groups) {
		t.Fatalf("第二遍迁移改了群配置：%d → %d", len(before.Groups), len(after.Groups))
	}
	for _, cfg := range groupStore.Groups().Groups {
		want, ok := groupEnabled(t, groupStore, cfg.BotProfileID, cfg.GroupID)
		if !ok || want != cfg.Enabled {
			t.Fatalf("群 %s 的开关被第二遍改了", cfg.GroupID)
		}
	}
}

// 多台机器人各迁各的：一台的白名单不该动到另一台的群。
func TestMigrateKeepsProfilesApart(t *testing.T) {
	profileStore, groupStore := migrationStores(t,
		assistant.BotConfig{ID: "a", BotAccount: "42", GroupAdmission: assistant.GroupAdmission{
			Mode: assistant.GroupAdmissionWhitelist, AllowedGroups: []string{"100"},
		}},
		assistant.BotConfig{ID: "b", BotAccount: "43"},
	)
	if _, err := groupStore.SaveGroupConfig(assistant.GroupConfig{
		BotProfileID: "b", GroupID: "100", Enabled: true, EnabledSet: true,
	}, profileStore.Profiles().Profiles[1]); err != nil {
		t.Fatal(err)
	}

	if err := MigrateGroupScopeSwitches(profileStore, groupStore); err != nil {
		t.Fatal(err)
	}

	if enabled, ok := groupEnabled(t, groupStore, "b", "100"); !ok || !enabled {
		t.Fatalf("B 在这个群的开关被 A 的白名单动了：enabled=%v configured=%v", enabled, ok)
	}
}

// 启动迁移要把快照清成跟随并落盘：之后机器人页改了值，群里跟着变。
func TestMigrateGroupInheritancePersistsBeforeBotChanges(t *testing.T) {
	bot := assistant.BotConfig{ID: "bot", Name: "Diana", GroupTriggers: []string{"Diana"}, MaxReplyChars: 800}.WithDefaults()
	profiles, groups := migrationStores(t, bot)
	// 按旧数据的样子直接塞进存储：没有迁移标记，触发词和回复上限都是机器人当时的值。
	groups.data = assistant.GroupConfigSet{Groups: []assistant.GroupConfig{{
		BotProfileID: "bot", GroupID: "1", Enabled: true, EnabledSet: true,
		GroupTriggers: []string{"Diana"}, MaxReplyChars: 800, RecentContextLimit: 12,
	}}}
	if err := MigrateGroupInheritance(profiles, groups); err != nil {
		t.Fatal(err)
	}
	stored := groups.Groups().Groups[0]
	if !stored.InheritanceMigrated || len(stored.GroupTriggers) != 0 || stored.MaxReplyChars != 0 {
		t.Fatalf("snapshot not cleared on disk: %+v", stored)
	}
	if stored.RecentContextLimit != 12 {
		t.Fatalf("real override lost: %d", stored.RecentContextLimit)
	}
	// 再跑一次什么也不动。
	if err := MigrateGroupInheritance(profiles, groups); err != nil {
		t.Fatal(err)
	}
}
