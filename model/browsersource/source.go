// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package browsersource 决定机器人用的是哪一个浏览器：Diana 内置的，还是用户
// 自己装了扩展的 Chrome，或者都不用。
//
// 这两者做的是同一件事——一个带登录态、只有主人能驱动、能点能输入的浏览器——
// 区别只在用谁的。所以它们互斥，同一时间只有一个生效。一次性无头渲染不在其中：
// 它不带登录态，读公开网页、出图、渲染 PDF 都靠它，一直可用，不需要选。
//
// 来源不单独落盘，而是从两边自己的总开关推出来：内置浏览器开着就是 Box，否则
// 扩展开着就是 Extension，都没开就是 Off。老配置升级不用迁移，界面上的选择和
// 两边的开关也永远对得上。切换来源时由调用方同时改这两个开关。
package browsersource

// 三种取值，和 WebUI 约定的字符串一致。
const (
	Off       = "off"
	Box       = "box"
	Extension = "extension"
)

// Resolve 按两边的总开关推出当前来源。两个都开着（旧版本允许这样）时内置浏览器
// 优先：它是默认推荐的那个，也是用户在 WebUI 里看得见画面的那个。
func Resolve(boxEnabled, extensionEnabled bool) string {
	switch {
	case boxEnabled:
		return Box
	case extensionEnabled:
		return Extension
	default:
		return Off
	}
}

// Valid 判断取值是否合法。
func Valid(source string) bool {
	switch source {
	case Off, Box, Extension:
		return true
	}
	return false
}
