// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"net/url"
	"strings"
)

const (
	// DefaultCommandTimeoutMS 是单条指令的默认等待上限。页面操作偶尔会慢，
	// 但等过一分钟基本就是用户已经切走或者页面卡住了，没必要继续挂着。
	DefaultCommandTimeoutMS = 20_000
	// MaxCommandTimeoutMS 挡住把超时填成一小时这种配置。
	MaxCommandTimeoutMS = 120_000
	// DefaultCommandsPerMinute 是每分钟指令数上限，防的是模型打转。
	DefaultCommandsPerMinute = 60
	// MaxCommandsPerMinute 是上限的上限。
	MaxCommandsPerMinute = 600
	// DefaultHeartbeatSeconds 是应用层心跳间隔。
	DefaultHeartbeatSeconds = 30
)

// Policy 是浏览器控制的授权边界。默认值（零值）是「什么都不能做」：
// 没启用、没有允许的站点、写操作关闭。要放开必须逐项显式打开，
// 这样忘记配置的后果是用不了，而不是敞开。
type Policy struct {
	// Enabled 是总开关。关掉之后已连接的扩展会被断开，工具也直接报不可用。
	Enabled bool `json:"enabled"`
	// AllowedOrigins 限制控制端来源，写扩展的 origin，例如
	// chrome-extension://abcdefghijklmnopabcdefghijklmnop。
	// 留空表示只接受没有 Origin 头的本地客户端（扩展 Service Worker 会带 Origin，
	// 所以留空等于不接受任何浏览器扩展），这是有意为之的失败关闭。
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	// AllowedHosts 是可操作站点白名单，支持 example.com 与 *.example.com。
	// 留空表示一个站点都不允许。
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	// DeniedHosts 优先于白名单。用来在放开一级域之后挖掉子域，
	// 例如放开 *.example.com 但排除 admin.example.com。
	DeniedHosts []string `json:"denied_hosts,omitempty"`
	// WriteEnabled 打开点击、输入与导航。关闭时只剩读取。
	WriteEnabled bool `json:"write_enabled"`
	// CommandTimeoutMS 单条指令等待上限。
	CommandTimeoutMS int `json:"command_timeout_ms,omitempty"`
	// CommandsPerMinute 每分钟指令数上限。
	CommandsPerMinute int `json:"commands_per_minute,omitempty"`
}

// PolicyDigest 是回给扩展的策略摘要。只给扩展需要自己拦一道的字段，
// 不含令牌，也不含别的连接信息。
type PolicyDigest struct {
	AllowedHosts      []string `json:"allowed_hosts"`
	DeniedHosts       []string `json:"denied_hosts"`
	WriteEnabled      bool     `json:"write_enabled"`
	CommandTimeoutMS  int      `json:"command_timeout_ms"`
	CommandsPerMinute int      `json:"commands_per_minute"`
}

// WithDefaults 补齐超时与限流，并把站点列表规范化。
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
	p.AllowedOrigins = normalizeOrigins(p.AllowedOrigins)
	p.AllowedHosts = normalizeHostPatterns(p.AllowedHosts)
	p.DeniedHosts = normalizeHostPatterns(p.DeniedHosts)
	return p
}

// Digest 返回回给扩展的策略摘要。
func (p Policy) Digest() PolicyDigest {
	p = p.WithDefaults()
	return PolicyDigest{
		AllowedHosts:      append([]string(nil), p.AllowedHosts...),
		DeniedHosts:       append([]string(nil), p.DeniedHosts...),
		WriteEnabled:      p.WriteEnabled,
		CommandTimeoutMS:  p.CommandTimeoutMS,
		CommandsPerMinute: p.CommandsPerMinute,
	}
}

// OriginAllowed 判断控制端来源是否在白名单内。空白名单一律拒绝，
// 包括不带 Origin 的请求：能连上控制面就能操作用户的浏览器，
// 这一档不留「没配就都放过」的默认。
func (p Policy) OriginAllowed(origin string) bool {
	origin = normalizeOrigin(origin)
	if origin == "" {
		return false
	}
	for _, allowed := range normalizeOrigins(p.AllowedOrigins) {
		if allowed == origin {
			return true
		}
	}
	return false
}

// HostAllowed 判断一个页面地址是否在可操作范围内。
// 只认 http 与 https：扩展页、本地文件和自定义协议一律不放。
func (p Policy) HostAllowed(rawURL string) bool {
	host, ok := policyHost(rawURL)
	if !ok {
		return false
	}
	for _, pattern := range normalizeHostPatterns(p.DeniedHosts) {
		if hostMatches(host, pattern) {
			return false
		}
	}
	for _, pattern := range normalizeHostPatterns(p.AllowedHosts) {
		if hostMatches(host, pattern) {
			return true
		}
	}
	return false
}

// policyHost 取出用于策略判断的主机名。带端口时只留主机部分：
// 同一个站点换端口不改变「这是谁的站点」这件事。
func policyHost(rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
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

// hostMatches 支持精确匹配和 *.example.com 形式的子域匹配。
// *.example.com 不包含 example.com 本身，想都放开就两条都写上，
// 免得「放开子域」被理解成「顺带放开主域」。
func hostMatches(host, pattern string) bool {
	if host == "" || pattern == "" {
		return false
	}
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

func normalizeHostPatterns(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.TrimSuffix(value, ".")
		if value == "" || seen[value] {
			continue
		}
		// 允许用户把整条地址粘进来，取其中的主机名，省掉一次「为什么不生效」。
		if strings.Contains(value, "/") {
			if host, ok := policyHost(value); ok {
				value = host
			} else {
				continue
			}
		}
		if value == "" || value == "*" || seen[value] {
			// 单独一个 * 等于放开全网，不接受：白名单得有名字。
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}
	origin = strings.TrimSuffix(origin, "/")
	// Origin 的 scheme 和 host 大小写不敏感，扩展 ID 本身是小写字母。
	return strings.ToLower(origin)
}

func normalizeOrigins(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeOrigin(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
