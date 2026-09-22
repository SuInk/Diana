// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 多行文本设置（工作区白名单、推送模板这类）以前没有归一化分支，保存时整体失败，
// 启动恢复时被当成非法值丢掉：主人在 WebUI 里填好的工作区，重启后就没了。
func TestPluginTextSettingSurvivesSaveAndRestore(t *testing.T) {
	specs := []PluginSettingSpec{{Key: "workspaces", Type: PluginSettingTypeText}}
	const value = "  diana=git@github.com:SuInk/Diana.git\nweb=/opt/example/web  "
	saved, err := normalizePluginSettings(specs, map[string]any{"workspaces": value})
	if err != nil {
		t.Fatalf("多行设置应该能保存：%v", err)
	}
	want := strings.TrimSpace(value)
	if saved["workspaces"] != want {
		t.Fatalf("保存后的值不对：%q", saved["workspaces"])
	}
	restored := sanitizePluginSettings(specs, map[string]any{"workspaces": want})
	if restored["workspaces"] != want {
		t.Fatalf("重启恢复后丢了多行设置：%#v", restored)
	}
	if _, err := normalizePluginSettings(specs, map[string]any{"workspaces": 42}); err == nil {
		t.Fatal("非字符串应该被拒绝")
	}
}

// 任何插件声明的设置类型都必须有归一化分支，否则那个设置存不住而且没有任何报错。
func TestEveryDeclaredPluginSettingTypeIsSupported(t *testing.T) {
	samples := map[string]any{
		PluginSettingTypeBool:               true,
		PluginSettingTypeNumber:             float64(1),
		PluginSettingTypeSize:               float64(1024),
		PluginSettingTypeString:             "value",
		PluginSettingTypeText:               "line one\nline two",
		PluginSettingTypeMultiSelect:        []any{},
		PluginSettingTypePlatformLevelRules: []any{},
		PluginSettingTypeCodingAgents:       []any{},
	}
	for _, state := range NewDefaultPluginManager().List() {
		manifest := state.Manifest
		for _, spec := range manifest.Settings {
			sample, ok := samples[spec.Type]
			if spec.Type == PluginSettingTypeSelect {
				if len(spec.Options) == 0 {
					t.Fatalf("%s 的设置 %q 是下拉框却没有选项", manifest.ID, spec.Key)
				}
				sample, ok = spec.Options[0].Value, true
			}
			if !ok {
				t.Fatalf("%s 的设置 %q 用了没有样例的类型 %q，请补上样例", manifest.ID, spec.Key, spec.Type)
			}
			if _, err := normalizeSettingValue(spec, sample); err != nil {
				t.Errorf("%s 的设置 %q（类型 %s）存不住：%v", manifest.ID, spec.Key, spec.Type, err)
			}
		}
	}
}
