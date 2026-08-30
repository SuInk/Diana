// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 群管理页上每个插件都能单独填参数，可运行时读设置有两个写法：带群级参数覆盖的
// 和不带的。用错后一个，界面填的参数会被静默丢掉——能填、能存、能显示，就是不
// 生效。这里从群配置一路走到插件真正拿到的值，把这根线钉住。
func TestGroupSettingOverridesReachPlugins(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"123456": {
			GroupID: "123456",
			PluginSettingOverrides: PluginSettingOverrides{
				imageOCRPluginID: {
					"backend":  imageOCRBackendLLM,
					"model":    "群里单独指定的视觉模型",
					"delivery": imageOCRDeliveryText,
				},
			},
		},
	}})

	group := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m-1"}
	_, cfg, ok := runtime.imageOCRActiveConfig(group)
	if !ok {
		t.Fatal("群里覆盖成可用的后端之后，插件应当被认为生效")
	}
	if cfg.Backend != imageOCRBackendLLM || cfg.Model != "群里单独指定的视觉模型" {
		t.Fatalf("群级参数没有传到插件：%#v", cfg)
	}
	if !cfg.textOnly() {
		t.Fatalf("群级 delivery 覆盖没有生效：%#v", cfg)
	}

	// 别的群不受影响：覆盖是按群走的，不是改了全局。
	other := MessageEvent{Kind: EventKindGroup, GroupID: "654321", UserID: "10001", MessageID: "m-2"}
	if _, _, ok := runtime.imageOCRActiveConfig(other); ok {
		t.Fatal("没有覆盖的群不该跟着开启")
	}
	// 私聊同理：它根本没有群配置。
	if _, _, ok := runtime.imageOCRActiveConfig(MessageEvent{Kind: EventKindPrivate, UserID: "10001"}); ok {
		t.Fatal("私聊不该拿到某个群的覆盖")
	}
}

// 群级开关和群级参数是同一张卡片的两半，代码里却是两个入参。这条守住「运行时
// 不再直接调只吃开关的那个重载」——漏传第二个参数不会报错，只会让参数悄悄失效，
// 靠人记住是记不住的。
func TestRuntimeReadsPluginSettingsThroughGroupAwareHelper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for index, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "PluginWithSettings(") {
				continue
			}
			// 事件驱动的读取必须走 pluginWithSettingsForEvent；只有和会话无关的
			// 全局读取（传 nil 或空事件）才允许用那个重载。
			if strings.Contains(line, "pluginOverridesForEvent(event)") {
				t.Errorf("%s:%d 直接调用了 PluginWithSettings，群级参数覆盖会被丢掉，请改用 pluginWithSettingsForEvent：%s",
					name, index+1, strings.TrimSpace(line))
			}
		}
	}
}

// 顺带确认助手用的那个包装本身是通的：没有群配置时不报错，也不凭空启用。
func TestPluginWithSettingsForEventWithoutGroupConfig(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	plugin, settings, enabled := runtime.pluginWithSettingsForEvent(imageOCRPluginID, MessageEvent{Kind: EventKindPrivate})
	if !enabled || plugin == nil {
		t.Fatal("内置插件默认启用，没有群配置时也该取得到")
	}
	if settings.String("backend", "") != imageOCRBackendDisabled {
		t.Fatalf("没有覆盖时应当拿到全局默认值：%q", settings.String("backend", ""))
	}
	var nilRuntime *Runtime
	if _, _, ok := nilRuntime.pluginWithSettingsForEvent(imageOCRPluginID, MessageEvent{}); ok {
		t.Fatal("运行时为空时不该报告启用")
	}
}
