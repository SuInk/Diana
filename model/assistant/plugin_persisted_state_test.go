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
// 现在落库只存用户改得动的部分：全局开关只留给 OpenAPI，manifest 由代码声明不落库。
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
		if _, ok := record["enabled"]; ok && id != OpenAPIPluginID {
			t.Fatalf("%s 落库时还带着看不见的全局开关", id)
		}
		if id == OpenAPIPluginID && record["enabled"] == nil {
			t.Fatal("OpenAPI 是进程级服务，全局开关要保留")
		}
	}
	if strings.Contains(string(data), `"manifest"`) {
		t.Fatal("快照里仍然有 manifest")
	}

	// 全局开关也不能再被设置：漏传机器人时要报错，而不是悄悄写一份没人看得见的值。
	if _, err := m.SetEnabled(statusCommandPluginID, true); err == nil {
		t.Fatal("没指定机器人时不该允许改开关")
	}
	if _, err := m.SetEnabled(OpenAPIPluginID, true); err != nil {
		t.Fatalf("OpenAPI 仍然要能全局开关：%v", err)
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
