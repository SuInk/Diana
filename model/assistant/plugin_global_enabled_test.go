// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 普通插件的开关只认按机器人那一份，存档里那个全局 Enabled 不再参与判定。
//
// 它曾经是总开关，迁移之后不再承载信息：每台已知机器人都会被写上明确条目，全局被
// 重置成清单默认。而插件页的开关只在选中机器人时渲染，用户点不到它，它却仍会出现
// 在状态接口里，把「全局关、这台开」显示成关着——线上就是这么让人误判的。
func TestOrdinaryPluginIgnoresStoredGlobalEnabled(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		codingAgentPluginID: {
			Installed:      true,
			Enabled:        false, // 存档里的全局值，应当被无视
			ProfileEnabled: map[string]bool{"on": true, "off": false},
		},
		imageOCRPluginID: {
			Installed: true,
			Enabled:   false, // 同样无视：没有按机器人设过就按清单默认
		},
	})

	state, ok := manager.Get(codingAgentPluginID)
	if !ok {
		t.Fatal("找不到编码代理")
	}
	if !state.ForProfile("on").Enabled {
		t.Fatal("这台机器人明确开着，却被判成关")
	}
	if state.ForProfile("off").Enabled {
		t.Fatal("这台机器人明确关着，却被判成开")
	}
	// 没设过的机器人按清单默认：编码代理是 default_disabled，所以是关。
	if state.ForProfile("never-set").Enabled {
		t.Fatal("默认关闭的插件在没设过的机器人上不该自己打开")
	}

	// 默认开启的插件反过来：存档全局关着也不影响，没设过就按清单默认开。
	ocr, ok := manager.Get(imageOCRPluginID)
	if !ok {
		t.Fatal("找不到 OCR 插件")
	}
	if ocr.Manifest.DefaultDisabled {
		t.Skip("OCR 插件改成默认关闭了，这条用例需要换一个默认开启的插件")
	}
	if !ocr.ForProfile("never-set").Enabled {
		t.Fatal("默认开启的插件被存档里的全局值关掉了")
	}
}

// 事件路径按机器人算出的覆盖说了算。没有覆盖时才退回存档里的全局值——配置里没有
// 机器人 ID 的老部署，那是唯一能写的开关，不能一起删掉。
func TestEnabledWithOverridesPrefersProfileDecision(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		codingAgentPluginID: {Installed: true, Enabled: true},
	})
	if !manager.EnabledWithOverrides(codingAgentPluginID, nil) {
		t.Fatal("没有机器人身份时应当仍按存档里的开关走")
	}
	if manager.EnabledWithOverrides(codingAgentPluginID, map[string]bool{codingAgentPluginID: false}) {
		t.Fatal("按机器人关闭没有生效")
	}
}

// 配置里没有机器人 ID 的部署，按空档案解析时不能把存档里的开关吃掉。
func TestProfilelessDeploymentKeepsStoredSwitch(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		codingAgentPluginID: {Installed: true, Enabled: true},
	})
	state, ok := manager.Get(codingAgentPluginID)
	if !ok {
		t.Fatal("找不到编码代理")
	}
	if !state.ForProfile("").Enabled {
		t.Fatal("空档案解析把老部署的开关判成了关")
	}
}

// OpenAPI 是进程级服务，不绑机器人，仍然按存档里的全局值走。
func TestOpenAPIKeepsProcessWideSwitch(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		OpenAPIPluginID: {Installed: true, Enabled: true},
	})
	state, ok := manager.Get(OpenAPIPluginID)
	if !ok {
		t.Fatal("找不到 OpenAPI 插件")
	}
	if !state.ForProfile("any").Enabled {
		t.Fatal("OpenAPI 的进程级开关被按机器人解析吃掉了")
	}
}
