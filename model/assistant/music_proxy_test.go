// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMusicProxyScopeCoversOnlyQQByDefault(t *testing.T) {
	cfg := musicProxyConfigFromSettings(SettingValues{musicSettingProxyMode: musicProxyCustom})
	for host, want := range map[string]bool{
		"ws.stream.qqmusic.qq.com": true,
		"u.y.qq.com":               true,
		"c6.y.qq.com.":             true,
		"music.163.com":            false,
		"notqq.com":                false,
	} {
		if got := cfg.covers(host); got != want {
			t.Fatalf("covers(%q) = %v, want %v", host, got, want)
		}
	}
	all := musicProxyConfigFromSettings(SettingValues{musicSettingProxyMode: musicProxySystem, musicSettingProxyScope: musicProxyScopeAll})
	if !all.covers("music.163.com") {
		t.Fatal("scope all should cover every source")
	}
	if off := musicProxyConfigFromSettings(SettingValues{musicSettingProxyScope: musicProxyScopeAll}); off.covers("u.y.qq.com") {
		t.Fatal("empty mode must keep the direct path")
	}
}

// 选了代理却拿不到地址时要报错，不能静默直连：直连的结果就是那个 403。
func TestMusicProxyRefusesToFallBackToDirect(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_PROXY", "")
	t.Setenv("RESOLVER_PROXY", "")
	fetcher := &musicFetcher{}
	for _, tc := range []struct {
		settings SettingValues
		want     string
	}{
		{SettingValues{musicSettingProxyMode: musicProxyCustom}, "代理地址没填"},
		{SettingValues{musicSettingProxyMode: musicProxyResolver}, "链接解析没填「解析代理」"},
		{SettingValues{musicSettingProxyMode: musicProxyCustom, musicSettingProxyURL: "ftp://user:hunter2@proxy.example:21"}, "只支持"},
	} {
		_, err := fetcher.httpClient(musicConfigFromSettings(tc.settings), true, "u.y.qq.com")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("httpClient(%v) error = %v, want %q", tc.settings, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("proxy password leaked into error: %v", err)
		}
	}
	// 只代理 QQ 音乐时，代理没配好不该连累网易云。
	if _, err := fetcher.httpClient(musicConfigFromSettings(SettingValues{musicSettingProxyMode: musicProxyCustom}), true, "music.163.com"); err != nil {
		t.Fatalf("netease should stay direct when only QQ is proxied: %v", err)
	}
}

func TestMusicProxyResolverModeUsesLinkParserProxy(t *testing.T) {
	proxy, seen := musicRecordingProxy(t, http.StatusPartialContent)
	fetcher := &musicFetcher{resolverProxy: func() string { return proxy }}
	cfg := musicConfigFromSettings(SettingValues{musicSettingProxyMode: musicProxyResolver, musicSettingProxyScope: musicProxyScopeAll})
	if !fetcher.probeAudio(context.Background(), cfg, "http://1.1.1.1/song.m4a", map[string]string{"Referer": "https://y.qq.com/"}) {
		t.Fatal("probe through the link parser proxy failed")
	}
	if got := <-seen; got.URL != "http://1.1.1.1/song.m4a" || got.Range != "bytes=0-1023" || got.Referer != "https://y.qq.com/" {
		t.Fatalf("proxy saw %#v", got)
	}
}

// 代理连不上和经代理仍被 CDN 拒绝是两种处理方式，提示要分开。
func TestMusicProxyFailuresAreDistinguishable(t *testing.T) {
	forbidden, _ := musicRecordingProxy(t, http.StatusForbidden)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String()
	listener.Close()

	for proxy, want := range map[string]string{forbidden: "代理出口不在中国大陆", closed: "代理可能连不上"} {
		cfg := musicConfigFromSettings(SettingValues{
			musicSettingProxyMode:  musicProxyCustom,
			musicSettingProxyURL:   proxy,
			musicSettingProxyScope: musicProxyScopeAll,
			musicSettingTimeout:    5,
		})
		ctx, diagnostics := withMusicDiagnostics(context.Background())
		if (&musicFetcher{}).probeAudio(ctx, cfg, "http://1.1.1.1/song.m4a", nil) {
			t.Fatalf("probe via %s unexpectedly succeeded", proxy)
		}
		if !strings.Contains(diagnostics.proxyProblem(), want) {
			t.Fatalf("proxy %s problem = %q, want %q", proxy, diagnostics.proxyProblem(), want)
		}
	}
}

// 没配代理时 QQ 的 403 也要点明是出口问题，不然用户会去换 Cookie。
func TestMusicDirectQQForbiddenSuggestsProxy(t *testing.T) {
	ctx, diagnostics := withMusicDiagnostics(context.Background())
	noteMusicProxyProblem(ctx, musicConfig{}, "https://ws.stream.qqmusic.qq.com/C400.m4a?vkey=x", http.StatusForbidden, nil)
	if !strings.Contains(diagnostics.proxyProblem(), "配置中国大陆出口的代理") {
		t.Fatalf("problem = %q", diagnostics.proxyProblem())
	}
	ctx, diagnostics = withMusicDiagnostics(context.Background())
	noteMusicProxyProblem(ctx, musicConfig{}, "https://m701.music.126.net/a.mp3", http.StatusForbidden, nil)
	if diagnostics.proxyProblem() != "" {
		t.Fatalf("netease 403 should not blame the exit: %q", diagnostics.proxyProblem())
	}
}

type musicProxiedRequest struct{ URL, Range, Referer string }

func musicRecordingProxy(t *testing.T, status int) (string, <-chan musicProxiedRequest) {
	t.Helper()
	seen := make(chan musicProxiedRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- musicProxiedRequest{URL: r.URL.String(), Range: r.Header.Get("Range"), Referer: r.Header.Get("Referer")}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("ID3"))
	}))
	t.Cleanup(server.Close)
	return server.URL, seen
}

// 选了沿用链接解析或系统代理时，设置页要说清代理实际落在哪、没配上时缺什么。
func TestMusicProxyStatusExplainsWhereTheProxyComesFrom(t *testing.T) {
	for _, name := range []string{"DIANA_RESOLVER_PROXY", "RESOLVER_PROXY", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	resolverProxy := ""
	plugin := NewMusicPlugin(nil)
	plugin.fetcher.resolverProxy = func() string { return resolverProxy }

	if got := plugin.ProxyStatus(SettingValues{}); !got.OK || got.Message != "未使用代理，所有曲库直连" {
		t.Fatalf("off = %#v", got)
	}
	if got := plugin.ProxyStatus(SettingValues{musicSettingProxyMode: musicProxyResolver}); got.OK || !strings.Contains(got.Message, "链接解析没填") {
		t.Fatalf("resolver unset = %#v", got)
	}
	resolverProxy = "socks5://user:hunter2@10.0.0.2:1080"
	got := plugin.ProxyStatus(SettingValues{musicSettingProxyMode: musicProxyResolver, musicSettingProxyScope: musicProxyScopeAll})
	if !got.OK || got.Address != "socks5://10.0.0.2:1080" || !strings.Contains(got.Message, "全部曲库") || !strings.Contains(got.Message, "链接解析") {
		t.Fatalf("resolver set = %#v", got)
	}
	if strings.Contains(got.Message+got.Address, "hunter2") {
		t.Fatalf("proxy password leaked: %#v", got)
	}
}
