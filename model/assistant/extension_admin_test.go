// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 从预设装一条 MCP 之后，缓存的共享底座必须扔掉重建。preset_save 在 agent 包内部才被
// 改写成 save，这一侧看到的仍是 preset_save——漏掉它的后果线上出现过：装好瑞幸之后模型
// 在同一个进程里翻遍 capabilities、list_capabilities、tools_load、extension_access 都
// 找不到它，8 格预算全花在找上，要等下次重启才生效。
func TestExtensionWriteChangesRegistry(t *testing.T) {
	for _, operation := range []string{"save", "preset_save", "delete"} {
		if !extensionWriteChangesRegistry(operation) {
			t.Fatalf("%s 改了扩展定义，应当重建底座", operation)
		}
	}
	// 这些只改按机器人存的开关和名单，每次请求重新读，不必重建底座。
	for _, operation := range []string{"list", "read", "presets", "preset_verify", "enabled", "members", "audience", "residency", "test", "preset_hide", "preset_show"} {
		if extensionWriteChangesRegistry(operation) {
			t.Fatalf("%s 没改扩展定义，不该把底座整个扔掉", operation)
		}
	}
}
