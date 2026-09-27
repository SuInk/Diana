// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 服务端下发的 redirect_host 不是腾讯域名时不能跟过去：之后的轮询、以及确认后
// 发 bot token 的请求都会打到那台主机上。
func TestWeixinLoginIgnoresUntrustedRedirectHost(t *testing.T) {
	fake := newFakeWeixinQRServer(t, `{"status":"scaned_but_redirect","redirect_host":"evil.example.com"}`, `{"status":"scaned"}`)
	m := newTestWeixinLogin(fake)
	ctx := context.Background()
	start, err := m.Start(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Poll(ctx, "p", start.SessionID, ""); got.Status != WeixinLoginScanned {
		t.Fatalf("redirect poll = %+v", got)
	}
	// 还在原来的服务器上轮询，说明没有跟去 evil.example.com。
	if got, _ := m.Poll(ctx, "p", start.SessionID, ""); got.Status != WeixinLoginScanned {
		t.Fatalf("poll after an untrusted redirect = %+v", got)
	}
	if len(fake.polledQR) != 2 {
		t.Fatalf("status endpoint was hit %d times, want both polls on the original host", len(fake.polledQR))
	}
}

// 可信的机房切换要照做：后续轮询换到新主机。
func TestWeixinLoginFollowsTrustedRedirectHost(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"status":"scaned"}`))
	}))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "https://")
	fake := newFakeWeixinQRServer(t, `{"status":"scaned_but_redirect","redirect_host":"`+targetHost+`"}`)
	m := newTestWeixinLogin(fake)
	// 测试里的「机房」是本机 TLS 服务器：放行它，并用信任它证书的客户端。
	m.trustHost = func(host string) bool { return host == "127.0.0.1" }
	m.client = target.Client()
	ctx := context.Background()
	start, err := m.Start(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = m.Poll(ctx, "p", start.SessionID, "")
	m.mu.Lock()
	base := m.sessions[start.SessionID].pollBase
	m.mu.Unlock()
	if base != "https://"+targetHost {
		t.Fatalf("poll base after trusted redirect = %q", base)
	}
	if got, _ := m.Poll(ctx, "p", start.SessionID, ""); got.Status != WeixinLoginScanned || hits.Load() != 1 {
		t.Fatalf("second poll = %+v, hits on redirect target = %d", got, hits.Load())
	}
}

// 确认时带回的 baseurl 同理：不是腾讯域名就丢掉，退回默认地址，token 不会发出去。
func TestWeixinLoginDropsUntrustedConfirmedBaseURL(t *testing.T) {
	fake := newFakeWeixinQRServer(t, `{"status":"confirmed","bot_token":"t","ilink_bot_id":"bot@im.bot","baseurl":"https://evil.example.com"}`)
	m := newTestWeixinLogin(fake)
	start, err := m.Start(context.Background(), "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m.Poll(context.Background(), "p", start.SessionID, "")
	if got.Credentials == nil || got.Credentials.BaseURL != "" {
		t.Fatalf("credentials = %+v, want the untrusted baseurl dropped", got.Credentials)
	}
}

func TestWeixinTrustedHost(t *testing.T) {
	for _, host := range []string{"ilinkai.weixin.qq.com", "novac2c.cdn.weixin.qq.com", "WEIXIN.QQ.COM"} {
		if !WeixinTrustedHost(host) {
			t.Errorf("%q should be trusted", host)
		}
	}
	for _, host := range []string{"evil.example.com", "weixin.qq.com.evil.com", "evilweixin.qq.com", "qq.com", ""} {
		if WeixinTrustedHost(host) {
			t.Errorf("%q should not be trusted", host)
		}
	}
	for raw, want := range map[string]bool{
		"https://ilinkai.weixin.qq.com":           true,
		"https://ilinkai.weixin.qq.com/":          true,
		"http://ilinkai.weixin.qq.com":            false,
		"https://user:pass@ilinkai.weixin.qq.com": false,
		"https://ilinkai.weixin.qq.com/x?y=1":     false,
		"https://evil.example.com":                false,
	} {
		if got := weixinTrustedBaseURL(raw); got != want {
			t.Errorf("weixinTrustedBaseURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

// 状态接口连不上时不能一直显示「等待扫码」：连续失败几次就明确报错。
func TestWeixinLoginReportsPersistentPollFailures(t *testing.T) {
	var statusCalls atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/get_bot_qrcode" {
			_, _ = w.Write([]byte(`{"qrcode":"qr","qrcode_img_content":"https://liteapp.weixin.qq.com/q/x"}`))
			return
		}
		statusCalls.Add(1)
		if failing.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"status":"wait"}`))
	}))
	defer server.Close()
	m := NewWeixinLoginManager()
	m.SetAPIBase(server.URL)
	ctx := context.Background()
	start, err := m.Start(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 偶发失败仍按等待处理，成功一次就清零。
	for i := 0; i < weixinQRMaxPollFailures-1; i++ {
		if got, _ := m.Poll(ctx, "p", start.SessionID, ""); got.Status != WeixinLoginWaiting {
			t.Fatalf("transient failure %d = %+v, want wait", i, got)
		}
	}
	failing.Store(false)
	if got, _ := m.Poll(ctx, "p", start.SessionID, ""); got.Status != WeixinLoginWaiting {
		t.Fatalf("recovered poll = %+v", got)
	}
	failing.Store(true)
	var last WeixinLoginStatus
	for i := 0; i < weixinQRMaxPollFailures; i++ {
		last, _ = m.Poll(ctx, "p", start.SessionID, "")
	}
	if last.Status != WeixinLoginFailed || !strings.Contains(last.Message, "连不上") {
		t.Fatalf("after %d consecutive failures status = %+v, want an explicit failure", weixinQRMaxPollFailures, last)
	}
	if got, _ := m.Poll(ctx, "p", start.SessionID, ""); got.Status != WeixinLoginExpired {
		t.Fatalf("failed session is still alive: %+v", got)
	}
}
