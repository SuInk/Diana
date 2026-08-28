// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package ghmirror 提供 GitHub 下载加速：把 github.com 的下载地址套上一层
// 公共反向代理前缀（https://镜像/https://github.com/...），并在「自动」模式下
// 实测各条线路，挑一条真的快的。
//
// 只有下载类地址走镜像。api.github.com 不走：公共代理对 API 的支持参差不齐，
// 而版本检查本身只有几 KB，直连失败也只是少一次提示，不值得把令牌和请求头
// 交给第三方。
package ghmirror

import (
	"errors"
	"net/url"
	"strings"
)

// Mode 是镜像选择策略。除了这两个常量，取值还可以是一个具体的镜像地址，
// 表示用户手动指定了线路。
const (
	// ModeAuto 实测直连和各镜像，挑一条快的。
	ModeAuto = "auto"
	// ModeDirect 始终直连 GitHub。
	ModeDirect = "direct"
)

// Mirror 是一条公共加速线路。
type Mirror struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
}

// builtinMirrors 是内置的候选线路。这些都是前缀式（把完整的 GitHub 地址接在
// 后面）的公共代理，与 NapCat 安装脚本采用的是同一批服务。
//
// 公共代理会失效、会限速、会换域名，所以这里只是候选：真正用哪条由实测决定，
// 全部不通时回落直连，任何一条挂掉都不会让更新流程失败。
var builtinMirrors = []Mirror{
	{Name: "ghfast.top", BaseURL: "https://ghfast.top"},
	{Name: "gh-proxy.com", BaseURL: "https://gh-proxy.com"},
	{Name: "gh-proxy.net", BaseURL: "https://gh-proxy.net"},
	{Name: "git.yylx.win", BaseURL: "https://git.yylx.win"},
	{Name: "ghfile.geekertao.top", BaseURL: "https://ghfile.geekertao.top"},
	{Name: "ghm.078465.xyz", BaseURL: "https://ghm.078465.xyz"},
	{Name: "gitproxy.127731.xyz", BaseURL: "https://gitproxy.127731.xyz"},
	{Name: "github.tbedu.top", BaseURL: "https://github.tbedu.top"},
}

// Builtin 返回内置候选线路的副本。
func Builtin() []Mirror {
	return append([]Mirror(nil), builtinMirrors...)
}

// acceleratedHosts 是前缀式代理能转发的 GitHub 下载域名。
var acceleratedHosts = map[string]bool{
	"github.com":                    true,
	"raw.githubusercontent.com":     true,
	"objects.githubusercontent.com": true,
	"codeload.github.com":           true,
	"gist.githubusercontent.com":    true,
}

// Accelerable 报告这个地址能不能套镜像前缀。
func Accelerable(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	return acceleratedHosts[strings.ToLower(parsed.Hostname())]
}

// Rewrite 把下载地址套上镜像前缀。base 为空、地址不是 GitHub 下载地址，
// 或者地址本身已经在镜像上，都原样返回——调用方可以拿返回值和入参比较，
// 相等就说明这次没有加速可用。
func Rewrite(base, raw string) string {
	raw = strings.TrimSpace(raw)
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || !Accelerable(raw) {
		return raw
	}
	return base + "/" + raw
}

// NormalizeBase 校验用户填写的镜像地址。只接受 https，且不带查询串和片段——
// 前缀式代理靠路径拼接工作，多出来的部分只会把后面的地址弄坏。
func NormalizeBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("ghmirror: 镜像地址为空")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", errors.New("ghmirror: 镜像地址必须使用 https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("ghmirror: 镜像地址缺少域名")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("ghmirror: 镜像地址不能带查询参数或锚点")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

// NormalizeMode 把用户提交的策略收敛成可用值：auto、direct，或者一条通过
// 校验的镜像地址。无法识别的值退回 auto，而不是让更新流程带着坏地址跑。
func NormalizeMode(raw string) string {
	value := strings.TrimSpace(raw)
	switch {
	case value == "" || strings.EqualFold(value, ModeAuto):
		return ModeAuto
	case strings.EqualFold(value, ModeDirect):
		return ModeDirect
	}
	base, err := NormalizeBase(value)
	if err != nil {
		return ModeAuto
	}
	return base
}
