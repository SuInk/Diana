// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package browsersource 决定机器人这一轮用哪个浏览器：Diana 内置的，还是用户自己
// 装了扩展的 Chrome。
//
// 两者做的是同一件事——带登录态、只有主人能驱动、能点能输入——区别只在用谁的。它们
// 可以同时开着，由用户排一个优先级：每一轮取排在最前、开着而且眼下用得上的那个，
// 前一个用不了（没装 Chrome、扩展没连上、正被人接管）就换下一个。模型每一轮只看到
// 一套浏览器工具，不会同时拿到两套、自己去猜该用哪个。
//
// 一次性无头渲染不在其中：它不带登录态，读公开网页、出图、渲染 PDF 都靠它，一直
// 可用，不需要选。
package browsersource

// 取值和 WebUI 约定的字符串一致。
const (
	Off       = "off"
	Box       = "box"
	Extension = "extension"
)

// DefaultOrder 是没排过时的优先级：内置浏览器在前，它不需要用户另装东西。
var DefaultOrder = []string{Box, Extension}

// Settings 是落盘的那部分：只有优先级。两边开没开各存在各自的配置里。
type Settings struct {
	Order []string `json:"order"`
}

// WithDefaults 规范化优先级：去掉不认识的和重复的，漏掉的按默认顺序补在后面。
func (s Settings) WithDefaults() Settings {
	seen := map[string]bool{}
	order := make([]string, 0, len(DefaultOrder))
	for _, source := range append(append([]string(nil), s.Order...), DefaultOrder...) {
		if Valid(source) && !seen[source] {
			seen[source] = true
			order = append(order, source)
		}
	}
	return Settings{Order: order}
}

// Pick 按优先级取第一个用得上的；一个都用不上时返回 Off。
func Pick(order []string, usable func(source string) bool) string {
	for _, source := range (Settings{Order: order}).WithDefaults().Order {
		if usable(source) {
			return source
		}
	}
	return Off
}

// Valid 判断是不是能排进优先级的来源。
func Valid(source string) bool {
	return source == Box || source == Extension
}
