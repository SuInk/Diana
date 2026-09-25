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
	"list", "read", "reveal", "presets", "verify", "test",
	"enabled", "members", "audience", "residency",
	"save", "delete",
}

// ExtensionOperationChangesDefinition 表示这次写入改了扩展的定义本身（装、改、卸）。
// 缓存的共享底座必须扔掉重建，否则新装的 MCP 要等下次重启才出现在工具目录里。
//
// 预设不再有自己的操作名：从预设装也是 save，只是多带了 preset/transport/values。
// 早先那套 preset_save 正是漏在这张名单外，装完要等重启才生效。
func ExtensionOperationChangesDefinition(operation string) bool {
	switch operation {
	case "save", "delete":
		return true
	default:
		return false
	}
}

// ExtensionRequestChangesDefinition 是按整个请求判断要不要重建底座。MCP 的启用开关
// 不改服务定义，却会改「哪些服务要起进程」：全局关着的服务，有机器人单独打开才起，
// 全局打开或关掉也要跟着起停。只看操作名的话，这些要等下次重启才生效。
func ExtensionRequestChangesDefinition(req ExtensionAdminRequest) bool {
	if ExtensionOperationChangesDefinition(req.Operation) {
		return true
	}
	return req.Operation == "enabled" && ExtensionKind(req.Kind) == ExtensionKindMCP
}

// ExtensionOperationMutatesState 表示这次调用改了某些会留下来的东西，值得记一条操作
// 日志。比上一个宽：启用开关、成员档位、对象名单、常驻档位、以及把预设从列表里藏起来
// 都算——它们不改扩展定义，但确实改了状态。
//
// 纯查询（list / read / reveal / verify）不算，test 也不算：它会真的连一次服务，但连完就断，
// 没有任何东西被改动。presets 的 hide / show 会改清单显隐，所以它按 action 另判，
// 见 ExtensionRequestMutatesState。
func ExtensionOperationMutatesState(operation string) bool {
	if ExtensionOperationChangesDefinition(operation) {
		return true
	}
	switch operation {
	case "enabled", "members", "audience", "residency":
		return true
	default:
		return false
	}
}

// ExtensionRequestMutatesState 是按整个请求判断，比只看操作名准：presets 默认是查询，
// 但 action=hide / show 会改清单显隐，那是写。调用方能拿到请求就用这个。
func ExtensionRequestMutatesState(req ExtensionAdminRequest) bool {
	if ExtensionOperationMutatesState(req.Operation) {
		return true
	}
	if req.Operation != "presets" {
		return false
	}
	switch req.Action {
	case "hide", "show":
		return true
	default:
		return false
	}
}
