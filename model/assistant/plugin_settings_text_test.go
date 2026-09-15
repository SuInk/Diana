// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 多行文本设置要能活过一次 Restore。
//
// 线上现象：编码代理的工作区白名单在界面上填好了、也存进了库，重启后工具却回
// 「还没有登记任何工作区，请在插件设置的工作区白名单里填写」。原因是
// normalizeSettingValue 没有 text 分支，落到 default 报「不支持的类型」，
// sanitizePluginSettings 于是把这个键整条跳过。
func TestTextSettingSurvivesRestore(t *testing.T) {
	manager := NewDefaultPluginManager()
	const workspaces = "diana=git@github.com:SuInk/Diana.git\nnotes=/Users/someone/notes"
	manager.Restore(map[string]PluginState{
		codingAgentPluginID: {
			Installed:      true,
			Enabled:        false,
			ProfileEnabled: map[string]bool{"p1": true},
			Settings: map[string]any{
				"backend":       "claude",
				"command":       "/opt/homebrew/bin/claude",
				"workspaces":    workspaces,
				"approval_mode": "dangerous",
			},
		},
	})

	state, ok := manager.Get(codingAgentPluginID)
	if !ok {
		t.Fatal("恢复后找不到编码代理")
	}
	if got, _ := state.Settings[codingAgentSettingWorkspaces].(string); got != workspaces {
		t.Fatalf("工作区白名单没活过 Restore：%q", got)
	}
	// 多行要原样保留，不能被压成一行。
	if strings.Count(state.Settings[codingAgentSettingWorkspaces].(string), "\n") != 1 {
		t.Fatal("多行文本的换行被吃掉了")
	}

	_, values, enabled := manager.PluginWithSettingsForProfile(codingAgentPluginID, "p1")
	if !enabled {
		t.Fatal("按档案开启的插件没有生效")
	}
	cfg, err := codingAgentConfigFromSettings(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Workspaces) != 2 {
		t.Fatalf("解析出 %d 个工作区，应当是 2 个", len(cfg.Workspaces))
	}
}

// 每一种已声明的设置类型都要能过清洗。漏一种就是「界面存得进、重启就没」。
func TestEverySettingTypeSurvivesSanitize(t *testing.T) {
	min, max := 0.0, 100.0
	for _, tc := range []struct {
		spec PluginSettingSpec
		raw  any
	}{
		{PluginSettingSpec{Key: "b", Type: PluginSettingTypeBool}, true},
		{PluginSettingSpec{Key: "n", Type: PluginSettingTypeNumber, Min: &min, Max: &max}, float64(5)},
		{PluginSettingSpec{Key: "s", Type: PluginSettingTypeString}, "值"},
		{PluginSettingSpec{Key: "t", Type: PluginSettingTypeText}, "第一行\n第二行"},
		{PluginSettingSpec{Key: "z", Type: PluginSettingTypeSize}, float64(1024)},
		{PluginSettingSpec{Key: "sel", Type: PluginSettingTypeSelect, Options: []PluginSettingOption{{Value: "a"}}}, "a"},
		{PluginSettingSpec{Key: "ms", Type: PluginSettingTypeMultiSelect, Options: []PluginSettingOption{{Value: "a"}}}, []string{"a"}},
	} {
		t.Run(tc.spec.Type, func(t *testing.T) {
			out := sanitizePluginSettings([]PluginSettingSpec{tc.spec}, map[string]any{tc.spec.Key: tc.raw})
			if _, ok := out[tc.spec.Key]; !ok {
				t.Fatalf("类型 %q 的值被清洗掉了，界面上存得进去、重启就没", tc.spec.Type)
			}
		})
	}
}
