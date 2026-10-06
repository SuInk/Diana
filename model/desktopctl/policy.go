// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import "strings"

const (
	DefaultCommandTimeoutMS  = 20_000
	MaxCommandTimeoutMS      = 120_000
	DefaultCommandsPerMinute = 60
	MaxCommandsPerMinute     = 600
	DefaultHeartbeatSeconds  = 30
)

// Policy 是桌面控制的授权边界。
//
// Enabled 默认关闭。打开之后，AllowedApps 为空表示允许全部应用（主人默认）；
// 非空则只允许名单内的 Bundle ID 或应用显示名。DeniedApps 优先于白名单。
// WriteEnabled 默认关闭；打开后才允许点击、输入与按键。截图与列窗口不依赖它。
type Policy struct {
	Enabled           bool     `json:"enabled"`
	AllowedApps       []string `json:"allowed_apps,omitempty"`
	DeniedApps        []string `json:"denied_apps,omitempty"`
	WriteEnabled      bool     `json:"write_enabled"`
	CommandTimeoutMS  int      `json:"command_timeout_ms,omitempty"`
	CommandsPerMinute int      `json:"commands_per_minute,omitempty"`
}

// PolicyDigest 是回给执行器的策略摘要。
type PolicyDigest struct {
	AllowedApps       []string `json:"allowed_apps"`
	DeniedApps        []string `json:"denied_apps"`
	WriteEnabled      bool     `json:"write_enabled"`
	CommandTimeoutMS  int      `json:"command_timeout_ms"`
	CommandsPerMinute int      `json:"commands_per_minute"`
}

// WithDefaults 补齐超时与限流，并规范化应用列表。
func (p Policy) WithDefaults() Policy {
	if p.CommandTimeoutMS <= 0 {
		p.CommandTimeoutMS = DefaultCommandTimeoutMS
	}
	if p.CommandTimeoutMS > MaxCommandTimeoutMS {
		p.CommandTimeoutMS = MaxCommandTimeoutMS
	}
	if p.CommandsPerMinute <= 0 {
		p.CommandsPerMinute = DefaultCommandsPerMinute
	}
	if p.CommandsPerMinute > MaxCommandsPerMinute {
		p.CommandsPerMinute = MaxCommandsPerMinute
	}
	p.AllowedApps = normalizeAppPatterns(p.AllowedApps)
	p.DeniedApps = normalizeAppPatterns(p.DeniedApps)
	return p
}

// Digest 返回回给执行器的策略摘要。
func (p Policy) Digest() PolicyDigest {
	p = p.WithDefaults()
	return PolicyDigest{
		AllowedApps:       append([]string(nil), p.AllowedApps...),
		DeniedApps:        append([]string(nil), p.DeniedApps...),
		WriteEnabled:      p.WriteEnabled,
		CommandTimeoutMS:  p.CommandTimeoutMS,
		CommandsPerMinute: p.CommandsPerMinute,
	}
}

// AppAllowed 判断一个窗口所属应用是否在可操作范围内。
//
// 与浏览器控制不同：桌面在 Enabled 且 AllowedApps 为空时允许全部应用；
// 这是主人默认「整机可读窗口」的语义。要收窄就显式填写 AllowedApps。
func (p Policy) AppAllowed(bundleID, appName string) bool {
	bundleID = normalizeAppID(bundleID)
	appName = normalizeAppID(appName)
	for _, pattern := range normalizeAppPatterns(p.DeniedApps) {
		if appMatches(bundleID, appName, pattern) {
			return false
		}
	}
	allowed := normalizeAppPatterns(p.AllowedApps)
	if len(allowed) == 0 {
		return true
	}
	for _, pattern := range allowed {
		if appMatches(bundleID, appName, pattern) {
			return true
		}
	}
	return false
}

func appMatches(bundleID, appName, pattern string) bool {
	if pattern == "" {
		return false
	}
	return bundleID == pattern || appName == pattern
}

func normalizeAppID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeAppPatterns(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeAppID(value)
		if value == "" || value == "*" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (p Policy) clone() Policy {
	p.AllowedApps = append([]string(nil), p.AllowedApps...)
	p.DeniedApps = append([]string(nil), p.DeniedApps...)
	return p
}
