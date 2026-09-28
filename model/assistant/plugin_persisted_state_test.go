// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// 插件开关在 WebUI 上一律按机器人来，全局开关既看不到也改不了。它却还在库里留着一份值，
// 排查时看到「enabled=false」会以为插件是关的，其实那个机器人的开关是开的。
// 现在落库只存用户改得动的部分：全局开关不再写，manifest 由代码声明不落库。
func TestPersistedPluginStateDropsInvisibleGlobalSwitch(t *testing.T) {
	m := NewDefaultPluginManager()
	if _, err := m.SetEnabledForProfile(statusCommandPluginID, "qq", true); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for id, record := range raw {
		if _, ok := record["manifest"]; ok {
			t.Fatalf("%s 落库时还带着 manifest", id)
		}
		if _, ok := record["enabled"]; ok {
			t.Fatalf("%s 落库时还带着看不见的全局开关", id)
		}
	}
	if strings.Contains(string(data), `"manifest"`) {
		t.Fatal("快照里仍然有 manifest")
	}

	// 全局开关也不能再被设置：漏传机器人时要报错，而不是悄悄写一份没人看得见的值。
	if _, err := m.SetEnabledForProfile(statusCommandPluginID, "", true); err == nil {
		t.Fatal("没指定机器人时不该允许改开关")
	}
}

// 升级前的数据里每个插件都存了全局开关，迁移要靠它给各机器人建初值，不能直接丢。
func TestLegacyGlobalSwitchStillSeedsProfileSwitches(t *testing.T) {
	enabled := true
	m := NewDefaultPluginManager()
	m.Restore(map[string]PersistedPluginState{
		statusCommandPluginID: {Installed: true, Enabled: &enabled},
	})
	if !m.MigrateProfileConfigurations([]BotConfig{{ID: "qq"}}) {
		t.Fatal("迁移没有执行")
	}
	if !m.EnabledWithOverrides(statusCommandPluginID, m.ProfileOverrides("qq")) {
		t.Fatal("升级前开着的插件在迁移后被关掉了")
	}
	record := m.Snapshot()[statusCommandPluginID]
	if record.Enabled != nil {
		t.Fatal("迁移之后不该再写全局开关")
	}
	if !record.ProfileEnabled["qq"] {
		t.Fatal("迁移没有把开关落到机器人上")
	}
}

// 已下线插件（如对外 API）的旧记录还会留在库里，恢复时要静默跳过，
// 不能报错，也不能凭空登记出一个目录里没有的插件。
func TestRestoreIgnoresRetiredPluginRecords(t *testing.T) {
	const retiredID = "official.open-api"
	enabled := true
	m := NewDefaultPluginManager()
	m.Restore(map[string]PersistedPluginState{
		retiredID:             {Installed: true, Enabled: &enabled, Settings: map[string]any{"rate_limit_per_minute": 60}},
		statusCommandPluginID: {Installed: true, ProfileEnabled: map[string]bool{"qq": true}, ProfileConfigMigrated: true},
	})
	if _, ok := m.Get(retiredID); ok {
		t.Fatal("已下线的插件不该被恢复出来")
	}
	if _, ok := m.Snapshot()[retiredID]; ok {
		t.Fatal("已下线的插件不该再写回库里")
	}
	if !m.EnabledWithOverrides(statusCommandPluginID, m.ProfileOverrides("qq")) {
		t.Fatal("旧记录影响了其他插件的恢复")
	}
	m.MigrateProfileConfigurations([]BotConfig{{ID: "qq"}})
	if _, ok := m.Get(retiredID); ok {
		t.Fatal("迁移不该把已下线的插件带回来")
	}
}
