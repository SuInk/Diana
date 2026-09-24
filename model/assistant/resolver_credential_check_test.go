// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostRewriteTransport 把发往各平台的请求改投到本地桩，原来的主机名放进
// X-Test-Host，桩按它分辨是哪家的账号接口。
type hostRewriteTransport struct{ target *url.URL }

func (t hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Test-Host", req.URL.Host)
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func newCredentialTestResolver(t *testing.T, handler http.HandlerFunc) *ResolverPlugin {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	plugin := NewResolverPlugin(nil)
	plugin.credentialClient = &http.Client{Transport: hostRewriteTransport{target: target}}
	return plugin
}

func credentialByKey(t *testing.T, checks []CredentialCheck, key string) CredentialCheck {
	t.Helper()
	for _, check := range checks {
		if check.Key == key {
			return check
		}
	}
	t.Fatalf("no credential check for %s in %#v", key, checks)
	return CredentialCheck{}
}

func TestResolverCredentialCheckReportsLoggedInAccounts(t *testing.T) {
	plugin := newCredentialTestResolver(t, func(w http.ResponseWriter, r *http.Request) {
		cookie := r.Header.Get("Cookie")
		switch r.Header.Get("X-Test-Host") {
		case "api.bilibili.com":
			if cookie != "SESSDATA=good" {
				t.Errorf("bilibili cookie = %q", cookie)
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"isLogin":true,"uname":"阿B","vipStatus":1}}`))
		case "www.douyin.com":
			// 抖音的风控先看 uifid 头，凭据测试得和真实解析带一样的头，
			// 否则测试过了、解析照样 403。
			if r.Header.Get("uifid") != "u1" {
				t.Errorf("douyin uifid header = %q", r.Header.Get("uifid"))
			}
			_, _ = w.Write([]byte(`{"message":"success","data":{"user_id":10001,"screen_name":"抖一抖"}}`))
		case "www.xiaohongshu.com":
			_, _ = w.Write([]byte(`<script>window.__INITIAL_STATE__={"global":{},"user":{"loggedIn":true,"activated":false,"userInfo":{"gender":0,"nickname":"小红","userId":undefined}}}</script>`))
		default:
			http.NotFound(w, r)
		}
	})
	checks := plugin.TestCredentials(context.Background(), SettingValues{
		resolverSettingBiliSessdata: "good",
		resolverSettingDouyinCookie: "sessionid=s; UIFID=u1",
		resolverSettingXHSCookie:    "a1=x; web_session=w",
	})
	for key, want := range map[string]string{
		resolverSettingBiliSessdata: "阿B",
		resolverSettingDouyinCookie: "抖一抖",
		resolverSettingXHSCookie:    "小红",
	} {
		check := credentialByKey(t, checks, key)
		if check.State != CredentialValid || check.Account != want {
			t.Fatalf("%s check = %#v, want valid account %q", key, check, want)
		}
	}
	if check := credentialByKey(t, checks, resolverSettingBiliSessdata); check.Message != "已登录，大会员" {
		t.Fatalf("bilibili message = %q", check.Message)
	}
	if check := credentialByKey(t, checks, resolverSettingYTDLPCookies); check.State != CredentialUnconfigured {
		t.Fatalf("yt-dlp check = %#v, want unconfigured", check)
	}
}

func TestResolverCredentialCheckExplainsLoggedOutCookies(t *testing.T) {
	plugin := newCredentialTestResolver(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Test-Host") {
		case "api.bilibili.com":
			_, _ = w.Write([]byte(`{"code":-101,"message":"账号未登录","data":{"isLogin":false}}`))
		case "www.douyin.com":
			_, _ = w.Write([]byte(`{"message":"error","data":{"description":"会话过期，请重新登录","error_code":13,"user_id":0}}`))
		case "www.xiaohongshu.com":
			_, _ = w.Write([]byte(`<script>window.__INITIAL_STATE__={"user":{"loggedIn":false,"userInfo":{"userId":undefined}}}</script>`))
		}
	})
	checks := plugin.TestCredentials(context.Background(), SettingValues{
		resolverSettingBiliSessdata: "SESSDATA=pasted-with-name",
		resolverSettingDouyinCookie: "ttwid=t",
		// 从 curl 的 -b '...' 里复制出来、又没有 web_session 的 Cookie：
		// 这正是生产上「明明给了 Cookie 却解析不了」的那份。
		resolverSettingXHSCookie: "'a1=x; webId=y'",
	})
	bili := credentialByKey(t, checks, resolverSettingBiliSessdata)
	if bili.State != CredentialInvalid || !strings.Contains(bili.Message, "只填 SESSDATA 的值") {
		t.Fatalf("bilibili check = %#v, want invalid with paste hint", bili)
	}
	douyin := credentialByKey(t, checks, resolverSettingDouyinCookie)
	if douyin.State != CredentialInvalid || !strings.Contains(douyin.Message, "会话过期") {
		t.Fatalf("douyin check = %#v", douyin)
	}
	xhs := credentialByKey(t, checks, resolverSettingXHSCookie)
	if xhs.State != CredentialInvalid || !strings.Contains(xhs.Message, "web_session") {
		t.Fatalf("xiaohongshu check = %#v, want invalid with web_session hint", xhs)
	}
}

func TestResolverCredentialCheckDoesNotBlameCookieForBlockedRequests(t *testing.T) {
	plugin := newCredentialTestResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "路由已封禁", http.StatusForbidden)
	})
	checks := plugin.TestCredentials(context.Background(), SettingValues{
		resolverSettingBiliSessdata: "x",
		resolverSettingDouyinCookie: "sessionid=s",
		resolverSettingXHSCookie:    "web_session=w",
	})
	for _, key := range []string{resolverSettingBiliSessdata, resolverSettingDouyinCookie, resolverSettingXHSCookie} {
		check := credentialByKey(t, checks, key)
		if check.State != CredentialError || !strings.Contains(check.Message, "判断不了") {
			t.Fatalf("%s check = %#v, want error that does not call the cookie invalid", key, check)
		}
	}
}

func TestYTDLPCookieFileCheck(t *testing.T) {
	dir := t.TempDir()
	netscape := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(netscape, []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonExport := filepath.Join(dir, "cookies.json")
	if err := os.WriteFile(jsonExport, []byte(`[{"name":"SID","value":"v"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		netscape:                      CredentialUnverified,
		jsonExport:                    CredentialInvalid,
		filepath.Join(dir, "missing"): CredentialInvalid,
		dir:                           CredentialInvalid,
	}
	for path, want := range cases {
		if got := checkYTDLPCookieFile(path); got.State != want {
			t.Fatalf("checkYTDLPCookieFile(%s) = %#v, want %s", filepath.Base(path), got, want)
		}
	}
}

func TestResolverCookieHeaderStripsCopiedQuotesAndHeaderName(t *testing.T) {
	for raw, want := range map[string]string{
		`'a1=x; web_session=w'`:       "a1=x; web_session=w",
		`"a1=x; web_session=w"`:       "a1=x; web_session=w",
		`Cookie: a1=x; web_session=w`: "a1=x; web_session=w",
		`a1=x; quoted="v"`:            `a1=x; quoted="v"`,
		`unread={"ub":"1"}; a1=x`:     `unread={"ub":"1"}; a1=x`,
	} {
		if got := sanitizeResolverCookieHeader(raw); got != want {
			t.Fatalf("sanitizeResolverCookieHeader(%q) = %q, want %q", raw, got, want)
		}
	}
}
