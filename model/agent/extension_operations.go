// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

// 扩展管理的操作名散在三处使用：AdministerExtensions 自己分发、assistant 决定要不要
// 重建共享底座、WebUI 决定这次调用记不记操作日志。三处各写一份名单，加了新操作就会漏
// 掉其中一两处——preset_save 就这么漏过：底座没重建，从预设装的 MCP 要等重启才出现；
// 同一份名单的另一头又把 presets、preset_verify 这种纯查询记成了「扩展管理操作已完成」。
//
// 所以把分类收到这一处，按两个互相独立的问题回答：
//   - 改没改扩展的定义本身（要重建底座）
//   - 改没改任何状态（要记操作日志）
// 新加操作时在这里归类一次，另外两处自然跟上；ExtensionOperations 保证不漏归类。

// ExtensionOperations 是 AdministerExtensions 支持的全部操作，测试据此检查分类是否漏项。
var ExtensionOperations = []string{
	"list", "read", "presets", "preset_verify", "test",
	"enabled", "members", "audience", "residency", "preset_hide", "preset_show",
	"save", "preset_save", "delete",
}

// ExtensionOperationChangesDefinition 表示这次写入改了扩展的定义本身（装、改、卸）。
// 缓存的共享底座必须扔掉重建，否则新装的 MCP 要等下次重启才出现在工具目录里。
//
// preset_save 要到 AdministerExtensions 内部才被改写成 save，调用方看到的始终是
// preset_save，所以它必须显式列在这里。
func ExtensionOperationChangesDefinition(operation string) bool {
	switch operation {
	case "save", "preset_save", "delete":
		return true
	default:
		return false
	}
}

// ExtensionOperationMutatesState 表示这次调用改了某些会留下来的东西，值得记一条操作
// 日志。比上一个宽：启用开关、成员档位、对象名单、常驻档位、以及把预设从列表里藏起来
// 都算——它们不改扩展定义，但确实改了状态。
//
// 纯查询（list / read / presets / preset_verify）和 test 不算。test 会真的连一次服务，
// 但连完就断，没有任何东西被改动。
func ExtensionOperationMutatesState(operation string) bool {
	if ExtensionOperationChangesDefinition(operation) {
		return true
	}
	switch operation {
	case "enabled", "members", "audience", "residency", "preset_hide", "preset_show":
		return true
	default:
		return false
	}
}
