// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 装、改、卸之后，缓存的共享底座必须扔掉重建。早先从预设装走的是另一个操作名
// preset_save，漏在这张名单外，后果线上出现过：装好瑞幸之后模型在同一个进程里翻遍
// capabilities、list_capabilities、tools_load、extension_access 都找不到它，8 格预算
// 全花在找上，要等下次重启才生效。现在预设不再有自己的操作名，从根上少一处要同步的
// 地方，这个用例继续盯着分类别再漏。
func TestExtensionWriteChangesRegistry(t *testing.T) {
	for _, operation := range []string{"save", "delete"} {
		if !agent.ExtensionOperationChangesDefinition(operation) {
			t.Fatalf("%s 改了扩展定义，应当重建底座", operation)
		}
	}
	// 这些只改按机器人存的开关和名单，每次请求重新读，不必重建底座。
	for _, operation := range []string{"list", "read", "presets", "verify", "enabled", "members", "audience", "residency", "test"} {
		if agent.ExtensionOperationChangesDefinition(operation) {
			t.Fatalf("%s 没改扩展定义，不该把底座整个扔掉", operation)
		}
	}
}
