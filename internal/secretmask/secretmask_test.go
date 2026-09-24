// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package secretmask

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 测试里的假凭据一律拆开拼接：公开仓库审计按 ghp_、sk- 前缀和「协议://用户:密码@」
// 形态找凭据，写成连续字面量会被当成真泄露拦下。

// Go 的 net/http 报错带着整条请求地址。这是 #756 里最隐蔽的一条：查询参数里的令牌
// 原样进了拼给模型的工具结果。这里构造一个真实的 *url.Error 来钉住。
func TestErrorMasksTokenInRealURLError(t *testing.T) {
	token := "qtok_" + "Zx81kLmN0pQr5sTu"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // 端口关掉，请求必然连不上。
	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+"/feed.xml?format=rss&access_token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, requestErr := client.Do(request)
	var urlErr *url.Error
	if !errors.As(requestErr, &urlErr) || !strings.Contains(requestErr.Error(), token) {
		t.Fatalf("前提不成立：报错里应当带着原始地址，实际 %v", requestErr)
	}
	masked := Error(requestErr)
	if strings.Contains(masked.Error(), token) {
		t.Fatalf("报错里仍有令牌原文：%s", masked)
	}
	if !strings.Contains(masked.Error(), "access_token="+Mask(token)) || !strings.Contains(masked.Error(), "format=rss") || !strings.Contains(masked.Error(), address) {
		t.Fatalf("地址其余部分应当原样保留，只遮令牌：%s", masked)
	}
	if !errors.As(masked, &urlErr) {
		t.Fatal("遮过的报错应当仍能 errors.As 出 *url.Error")
	}
}

func TestURLsMasksEmbeddedCredentials(t *testing.T) {
	password := "pw-" + "9fK2mQ7xL0aZ"
	userToken := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123"
	botToken := "123456789:" + "AAHd83kLmNoPqRsTuVwXyZ0123456789"
	apiKey := "key-" + "5f0c9a8b7e6d5c4b"
	cases := []struct {
		in       string
		secrets  []string
		keep     []string
		unmasked bool
	}{
		{in: "dial https://" + "alice:" + password + "@proxy.example:8080/x", secrets: []string{password}, keep: []string{"proxy.example:8080/x"}},
		{in: `Get "https://` + userToken + `@github.com/o/r.git": EOF`, secrets: []string{userToken}, keep: []string{"@github.com/o/r.git"}},
		{in: "POST https://api.telegram.org/bot" + botToken + "/sendMessage failed", secrets: []string{strings.TrimPrefix(botToken, "123456789:")}, keep: []string{"/bot123456789:", "/sendMessage"}},
		{in: "GET https://api.example/v1/models?key=" + apiKey + "&pageSize=1000#frag", secrets: []string{apiKey}, keep: []string{"pageSize=1000#frag"}},
		{in: "ws://127.0.0.1:3001/?access_token=" + apiKey, secrets: []string{apiKey}, keep: []string{"ws://127.0.0.1:3001/?access_token="}},
		{in: "https://example.com/search?q=golang&page=2", keep: []string{"https://example.com/search?q=golang&page=2"}, unmasked: true},
	}
	for _, tc := range cases {
		got := URLs(tc.in)
		for _, secret := range tc.secrets {
			if strings.Contains(got, secret) {
				t.Fatalf("URLs(%q) = %q，仍有原文", tc.in, got)
			}
		}
		for _, keep := range tc.keep {
			if !strings.Contains(got, keep) {
				t.Fatalf("URLs(%q) = %q，丢了 %q", tc.in, got, keep)
			}
		}
		if tc.unmasked && got != tc.in {
			t.Fatalf("不带凭据的地址不该被改：%q → %q", tc.in, got)
		}
	}
}

func TestHeadersMasksAuthorizationAndCookie(t *testing.T) {
	bearer := "sk-" + "live0123456789abcdefghij"
	cookie := "SESSDATA=" + "a1b2c3d4e5f6g7h8" + "; bili_jct=" + "0f9e8d7c6b5a4321"
	text := "upstream echoed: Authorization: Bearer " + bearer + "\nCookie: " + cookie + "\nX-Api-Key=" + bearer
	got := Headers(text)
	for _, secret := range []string{bearer, "a1b2c3d4e5f6g7h8", "0f9e8d7c6b5a4321"} {
		if strings.Contains(got, secret) {
			t.Fatalf("请求头里的凭据没遮：%q", got)
		}
	}
	if !strings.Contains(got, "Authorization: Bearer ") {
		t.Fatalf("认证方案应当保留，便于认出是哪种凭据：%q", got)
	}
}

func TestKnownMasksRegisteredSecretsAndVariants(t *testing.T) {
	apiKey := "sk-" + "reg0123456789abcdefghijk"
	cookieValue := "cv" + "0123456789abcdef"
	oddToken := "tok/" + "with+plus=and/slash"
	Register(apiKey, "douyin_webid=1; sid_guard="+cookieValue+"; ttwid=1", oddToken, "short")
	text := "gateway said: invalid key " + apiKey + "; cookie " + cookieValue + " url " + url.QueryEscape(oddToken)
	got := Known(text)
	for _, secret := range []string{apiKey, cookieValue, url.QueryEscape(oddToken)} {
		if strings.Contains(got, secret) {
			t.Fatalf("已登记的凭据没遮：%q", got)
		}
	}
	if !strings.Contains(Known("a short word"), "short") {
		t.Fatal("太短的值不该登记，否则正常正文会被改坏")
	}
	Register("20260924" + "01")
	if got := Known("群 2026092401 的消息"); !strings.Contains(got, "2026092401") {
		t.Fatalf("纯数字短值和群号、消息 ID 撞车，不该登记：%q", got)
	}
}

// 正常输出只遮已登记原文和 userinfo：网页里签名链接的查询参数模型还要接着用。
func TestOutputKeepsSignedLinksButMasksUserinfo(t *testing.T) {
	password := "pw-" + "Output0123456"
	signed := "https://cdn.example/v.mp4?signature=abcdef0123456789&expires=1"
	text := "video " + signed + " proxy http://" + "u:" + password + "@10.0.0.1:7890"
	got := Output(text)
	if !strings.Contains(got, signed) {
		t.Fatalf("签名链接不该被改：%q", got)
	}
	if strings.Contains(got, password) {
		t.Fatalf("地址里的密码没遮：%q", got)
	}
}

func TestRegisterURLRegistersOnlyEmbeddedCredentials(t *testing.T) {
	token := "feedtok" + "0123456789ab"
	RegisterURL("https://git.example/o/r.rss?token=" + token + "&lang=zh")
	got := Known("抓取失败：token 是 " + token + "，站点 git.example")
	if strings.Contains(got, token) {
		t.Fatalf("地址里的令牌应当登记：%q", got)
	}
	if !strings.Contains(got, "git.example") {
		t.Fatalf("地址本身不是秘密，不该登记：%q", got)
	}
}

func TestSensitiveName(t *testing.T) {
	for _, name := range []string{"TAVILY_API_KEY", "GITHUB_TOKEN", "DIANA_BILI_SESSDATA", "DOUYIN_CK", "access_token", "api-key", "key", "client_secret", "DB_PWD", "DIANA_CODEX_KEY", "signature"} {
		if !SensitiveName(name) {
			t.Errorf("%s 应当算凭据", name)
		}
	}
	for _, name := range []string{"PWD", "OLDPWD", "PATH", "HOME", "DIANA_SECRETS_FILE", "TOKEN_URL", "XDG_SESSION_TYPE", "DBUS_SESSION_BUS_ADDRESS", "SSH_AUTH_SOCK", "keyword", "monkey", "author", "page"} {
		if SensitiveName(name) {
			t.Errorf("%s 不该算凭据", name)
		}
	}
}

func TestMaskShape(t *testing.T) {
	if got := Mask("short"); got != Marker {
		t.Fatalf("短值应当整个遮掉：%q", got)
	}
	if got := Mask("Bearer " + "abcdefghijklmnopqrstuvwxyz"); got != "Bearer abcd****wxyz" {
		t.Fatalf("认证方案应当保留：%q", got)
	}
}
