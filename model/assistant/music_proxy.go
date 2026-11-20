// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/SuInk/diana/model/netguard"
)

// 音乐代理只为一件事：QQ 音乐的 CDN 在边缘按出口 IP 拒绝海外请求（403、空正文），
// 换 Cookie、换自建 API 都绕不过去——音频始终是 Diana 自己下的。所以代理挂在
// Diana 的对外请求上，按目标域名决定走不走：默认只有 QQ 音乐的域名走，网易云
// 不受影响。管理员自己的自建 API 不走代理，它多半就在 127.0.0.1。

const (
	musicSettingProxyMode  = "proxy_mode"
	musicSettingProxyURL   = "proxy_url"
	musicSettingProxyScope = "proxy_scope"

	musicProxyOff      = ""
	musicProxyCustom   = "custom"
	musicProxyResolver = "resolver"
	musicProxySystem   = "system"

	musicProxyScopeQQ  = "qq"
	musicProxyScopeAll = "all"
)

type musicProxyConfig struct {
	Mode  string
	URL   string
	Scope string
}

func musicProxyConfigFromSettings(settings SettingValues) musicProxyConfig {
	mode := strings.TrimSpace(settings.String(musicSettingProxyMode, musicProxyOff))
	switch mode {
	case musicProxyCustom, musicProxyResolver, musicProxySystem:
	default:
		mode = musicProxyOff
	}
	scope := strings.TrimSpace(settings.String(musicSettingProxyScope, musicProxyScopeQQ))
	if scope != musicProxyScopeAll {
		scope = musicProxyScopeQQ
	}
	return musicProxyConfig{Mode: mode, URL: strings.TrimSpace(settings.String(musicSettingProxyURL, "")), Scope: scope}
}

func (c musicProxyConfig) enabled() bool { return c.Mode != musicProxyOff }

// covers 判断这个目标域名在不在代理范围里。
func (c musicProxyConfig) covers(host string) bool {
	if !c.enabled() {
		return false
	}
	return c.Scope == musicProxyScopeAll || musicQQHost(host)
}

// musicQQHost 认 QQ 音乐用到的域名：接口在 u.y.qq.com，音频在
// ws/isure/dl.stream.qqmusic.qq.com，分享短链在 c6.y.qq.com，都在 qq.com 下面。
func musicQQHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "qq.com" || strings.HasSuffix(host, ".qq.com")
}

// proxyFunc 把配置变成 netguard 用的代理选择器。返回 nil 表示不走代理。
//
// 选了代理来源却拿不到地址时报错，不静默直连：直连的结果就是用户最初看到的那个
// 403，看上去像代理没起作用。
func (f *musicFetcher) proxyFunc(cfg musicConfig) (netguard.ProxyFunc, error) {
	proxy := cfg.Proxy
	if !proxy.enabled() {
		return nil, nil
	}
	chosen, _, err := f.effectiveProxy(cfg)
	if err != nil {
		return nil, err
	}
	return func(req *http.Request) (*url.URL, error) {
		if !proxy.covers(req.URL.Hostname()) {
			return nil, nil
		}
		if proxy.Mode == musicProxySystem {
			// 每个请求按环境变量重新挑，NO_PROXY 才照常生效。
			return http.ProxyFromEnvironment(req)
		}
		return chosen, nil
	}, nil
}

// effectiveProxy 返回这次实际会用的代理地址和它的出处。
func (f *musicFetcher) effectiveProxy(cfg musicConfig) (*url.URL, string, error) {
	switch cfg.Proxy.Mode {
	case musicProxyCustom:
		if strings.TrimSpace(cfg.Proxy.URL) == "" {
			return nil, "", errors.New("音乐代理选了自定义地址，但代理地址没填")
		}
		parsed, err := parseMusicProxyURL(cfg.Proxy.URL)
		return parsed, "自定义地址", err
	case musicProxyResolver:
		raw, origin := f.resolverProxyURL()
		if raw == "" {
			return nil, "", errors.New("音乐代理选了沿用链接解析的代理，但链接解析没填「解析代理」，也没有 DIANA_RESOLVER_PROXY 环境变量")
		}
		parsed, err := parseMusicProxyURL(raw)
		return parsed, origin, err
	case musicProxySystem:
		// 用一个 QQ 音乐地址问环境变量，HTTPS_PROXY、HTTP_PROXY、ALL_PROXY 和 NO_PROXY 的规则都照标准库来。
		probe, _ := http.NewRequest(http.MethodGet, "https://u.y.qq.com/", nil)
		parsed, err := http.ProxyFromEnvironment(probe)
		if err != nil {
			return nil, "", errors.New("系统代理环境变量格式不对")
		}
		if parsed == nil {
			return nil, "", errors.New("音乐代理选了系统代理，但进程环境变量里没有 HTTPS_PROXY / HTTP_PROXY，或 QQ 音乐域名被 NO_PROXY 排除")
		}
		return parsed, "系统代理环境变量", nil
	}
	return nil, "", nil
}

// resolverProxyURL 取链接解析的「解析代理」，和解析那边一样，没填时回退环境变量。
func (f *musicFetcher) resolverProxyURL() (string, string) {
	if f != nil && f.resolverProxy != nil {
		if value := strings.TrimSpace(f.resolverProxy()); value != "" {
			return value, "链接解析的「解析代理」"
		}
	}
	for _, name := range []string{"DIANA_RESOLVER_PROXY", "RESOLVER_PROXY"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, "环境变量 " + name
		}
	}
	return "", ""
}

// MusicProxyStatus 告诉设置页代理实际落在哪：地址只给协议、主机和端口，不回显账号密码。
type MusicProxyStatus struct {
	Mode    string `json:"mode"`
	OK      bool   `json:"ok"`
	Address string `json:"address,omitempty"`
	Message string `json:"message"`
}

// ProxyStatus 只看配置，不发请求；代理通不通看各家曲库的下载实测。
func (p *MusicPlugin) ProxyStatus(settings SettingValues) MusicProxyStatus {
	cfg := musicConfigFromSettings(settings)
	status := MusicProxyStatus{Mode: cfg.Proxy.Mode}
	if !cfg.Proxy.enabled() {
		status.OK = true
		status.Message = "未使用代理，所有曲库直连"
		return status
	}
	chosen, origin, err := p.fetcher.effectiveProxy(cfg)
	if err != nil {
		status.Message = err.Error()
		return status
	}
	status.OK = true
	status.Address = (&url.URL{Scheme: chosen.Scheme, Host: chosen.Host}).String()
	scope := "QQ 音乐"
	if cfg.Proxy.Scope == musicProxyScopeAll {
		scope = "全部曲库"
	}
	status.Message = fmt.Sprintf("%s经 %s 出站（来自%s）", scope, status.Address, origin)
	return status
}

func parseMusicProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	// 报错里不带原文：地址里可能嵌着代理密码。
	if err != nil {
		return nil, errors.New("音乐代理地址格式不对")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("音乐代理只支持 http、https、socks5、socks5h")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("音乐代理地址缺少主机")
	}
	return parsed, nil
}

// noteMusicProxyProblem 把代理相关的失败翻成能照着做的一句话，只记第一次。
func noteMusicProxyProblem(ctx context.Context, cfg musicConfig, endpoint string, status int, transportErr error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return
	}
	proxied := cfg.Proxy.covers(parsed.Hostname())
	switch {
	case proxied && transportErr != nil:
		recordMusicProxyProblem(ctx, fmt.Sprintf("经音乐代理请求失败，代理可能连不上或拒绝转发：%v", transportErr))
	case proxied && status == http.StatusForbidden:
		recordMusicProxyProblem(ctx, "已经过音乐代理，但 CDN 仍返回 403，代理出口不在中国大陆；香港、新加坡、日本等出口不一定能绕过，请换中国大陆出口的代理")
	case !proxied && status == http.StatusForbidden && musicQQHost(parsed.Hostname()):
		recordMusicProxyProblem(ctx, "QQ 音乐 CDN 返回 403，部署机出口不在中国大陆时会被拒绝，可在音乐增强设置里配置中国大陆出口的代理")
	}
}
