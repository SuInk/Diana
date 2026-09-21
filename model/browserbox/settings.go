// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package browserbox 管理 Diana 自己的那个浏览器：一个常驻的 Chrome/Chromium
// 进程，profile 落在数据目录里，登录态跨重启保留。
//
// 它和另外两档不是一回事，三档各有各的用处：
//
//   - browser_*（一次性无头浏览器）：每次全新 profile，读公开网页、出图，不带登录态。
//   - browser_ext_*（浏览器控制扩展，model/browserctl）：用户日常浏览器里的扩展，
//     带着用户自己的登录态，受扩展权限和站点白名单双重约束。
//   - 本包（内置浏览器）：Diana 自己的浏览器。用户在 WebUI 里能看见画面、能直接
//     上手点，在里面登录一次，登录态就留在挂出来的 profile 目录里。
//
// 边界：内置浏览器里的登录态是用户亲手建立的，所以这里不做站点白名单那种
// 「默认一个站都不许」的失败关闭——那会让这一档没法用来浏览。取而代之的是
// 黑名单加人工接管：接管打开时模型一条指令都下不去，用户自己点。
package browserbox

import (
	"net/url"
	"strings"
)

const (
	// DefaultWindowWidth/Height 是画面尺寸。它同时是截图和实时画面的分辨率，
	// 挑的是能放下大多数桌面站点、又不至于让每帧 JPEG 太大的一档。
	DefaultWindowWidth  = 1280
	DefaultWindowHeight = 800
	// MinWindowSide/MaxWindowSide 挡住把窗口设成 1 像素或者 8K。
	MinWindowSide = 320
	MaxWindowSide = 2560
)

// Settings 是内置浏览器的配置。零值是「关着」：装了 Diana 不等于多出一个浏览器。
type Settings struct {
	// Enabled 决定进程起不起。关掉会当场结束进程，模型那一侧的 CDP 地址也随之消失。
	Enabled bool `json:"enabled"`
	// Headful 决定要不要开一个真窗口。默认无头，这是唯一能在容器里跑起来的模式，
	// 而且无头不影响实时画面——画面走的是 CDP 的 screencast，不是截屏。
	// 写成「有头」而不是「无头」是因为零值必须是能用的那一档：容器里默认
	// headless=false 的话，用户一打开开关就撞上「没有显示器」。
	Headful bool `json:"headful,omitempty"`
	// WindowWidth/WindowHeight 是渲染尺寸。
	WindowWidth  int `json:"window_width,omitempty"`
	WindowHeight int `json:"window_height,omitempty"`
	// DeniedHosts 是永远不许打开的站点，支持 example.com 与 *.example.com。
	// 这一档没有白名单：内置浏览器的用途就是浏览，白名单留空即全禁会让它没法用。
	DeniedHosts []string `json:"denied_hosts,omitempty"`
	// Executable 指定浏览器可执行文件，留空时自动查找。
	Executable string `json:"executable,omitempty"`
}

// WithDefaults 补齐尺寸并规范化黑名单。
func (s Settings) WithDefaults() Settings {
	if s.WindowWidth <= 0 {
		s.WindowWidth = DefaultWindowWidth
	}
	if s.WindowHeight <= 0 {
		s.WindowHeight = DefaultWindowHeight
	}
	s.WindowWidth = clampSide(s.WindowWidth)
	s.WindowHeight = clampSide(s.WindowHeight)
	s.DeniedHosts = normalizeHosts(s.DeniedHosts)
	s.Executable = strings.TrimSpace(s.Executable)
	return s
}

func clampSide(value int) int {
	if value < MinWindowSide {
		return MinWindowSide
	}
	if value > MaxWindowSide {
		return MaxWindowSide
	}
	return value
}

// HostAllowed 判断一个地址能不能在内置浏览器里打开。只认 http 和 https：
// file:// 会让浏览器读到容器里的文件，chrome:// 能翻出浏览器自己的设置页。
func (s Settings) HostAllowed(rawURL string) bool {
	host, ok := policyHost(rawURL)
	if !ok {
		return false
	}
	for _, pattern := range normalizeHosts(s.DeniedHosts) {
		if hostMatches(host, pattern) {
			return false
		}
	}
	return true
}

func policyHost(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return "", false
	}
	return strings.TrimSuffix(host, "."), true
}

// hostMatches 与 model/browserctl 同规则：*.example.com 匹配子域但不含主域本身。
func hostMatches(host, pattern string) bool {
	if host == "" || pattern == "" {
		return false
	}
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

func normalizeHosts(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.TrimSuffix(value, ".")
		if strings.Contains(value, "/") {
			if host, ok := policyHost(value); ok {
				value = host
			} else {
				continue
			}
		}
		if value == "" || value == "*" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
