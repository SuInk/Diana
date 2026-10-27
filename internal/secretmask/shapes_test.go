// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package secretmask

import (
	"regexp"
	"strings"
	"testing"
)

// 令牌用拼接造出来，免得仓库里出现能被密钥扫描当真的整串。
var (
	fakeOpenAIKey   = "sk-" + strings.Repeat("a1B2", 6)
	fakeGitHubToken = "ghp" + "_" + strings.Repeat("Zx9", 12)
	fakeAWSKey      = "AKIA" + strings.Repeat("Q7", 8)
	fakeJWT         = "eyJ" + strings.Repeat("h", 12) + "." + strings.Repeat("p", 12) + "." + strings.Repeat("s", 12)
)

func TestTokenPattern(t *testing.T) {
	for _, token := range []string{fakeOpenAIKey, fakeGitHubToken, fakeAWSKey, fakeJWT, "admin-" + strings.Repeat("0f", 12)} {
		text := "upstream said: " + token + " (retry later)"
		if got := TokenPattern.ReplaceAllString(text, "[T]"); got != "upstream said: [T] (retry later)" {
			t.Errorf("%s 没被认出来：%q", token[:4], got)
		}
	}
	for _, text := range []string{"task-scheduler", "ghp_short", "risk-free", "AKIA"} {
		if TokenPattern.MatchString(text) {
			t.Errorf("%q 不是令牌", text)
		}
	}
}

func TestBearerPattern(t *testing.T) {
	cases := map[string]string{
		"bearer " + strings.Repeat("x", 16):     "bearer [R]",
		"Authorization: Bearer abc.def_ghi-jkl": "Authorization: Bearer [R]",
		"Basic dXNlcjpwYXNzd29yZA==":            "Basic [R]",
		// 小写的 basic 是普通英文，不是请求头写法。
		"needs basic authentication": "needs basic authentication",
		"Bearer short":               "Bearer short",
	}
	for input, want := range cases {
		if got := BearerPattern.ReplaceAllString(input, "$1 [R]"); got != want {
			t.Errorf("%q → %q，期望 %q", input, got, want)
		}
	}
}

func TestCredentialPatterns(t *testing.T) {
	redact := func(text string) string {
		text = QuotedCredentialPattern.ReplaceAllString(text, `${1}"[R]"`)
		text = AuthorizationPattern.ReplaceAllString(text, "Authorization=[R]")
		return CredentialPattern.ReplaceAllString(text, "${1}=[R]")
	}
	cases := map[string]string{
		`{"api_key":"abc\"def","model":"m"}`: `{"api_key":"[R]","model":"m"}`,
		`{'client_secret': 'xyz'}`:           `{'client_secret': "[R]"}`,
		`password = "two words" next`:        `password=[R] next`,
		`GITHUB_TOKEN=abc123&page=2`:         `GITHUB_TOKEN=[R]&page=2`,
		`x-api-key: k1,k2`:                   `x-api-key=[R],k2`,
		`Authorization: token abc123`:        `Authorization=[R]`,
		`(token=abc)`:                        `(token=[R])`,
		`max_tokens=4096 model=gpt`:          `max_tokens=4096 model=gpt`,
		`GitHub Token 无效或已过期`:                `GitHub Token 无效或已过期`,
		`已遮过的 token=[REDACTED] 不再动`:          `已遮过的 token=[REDACTED] 不再动`,
	}
	for input, want := range cases {
		if got := redact(input); got != want {
			t.Errorf("%q → %q，期望 %q", input, got, want)
		}
	}
}

// 先换 Authorization 再按键名换，已经换好的那段不能再被认出来。
func TestAuthorizationThenCredentialIsStable(t *testing.T) {
	text := AuthorizationPattern.ReplaceAllString("Authorization: Bearer abcdefgh123", "Authorization=[REDACTED]")
	if got := CredentialPattern.ReplaceAllString(text, "${1}=[REDACTED]"); got != "Authorization=[REDACTED]" {
		t.Fatalf("重复替换：%q", got)
	}
}

func TestPrivateKeyPattern(t *testing.T) {
	// 拼出来，免得公开仓库审计把测试样本当成私钥。
	begin, end := "-----BEGIN OPENSSH "+"PRIVATE KEY-----", "-----END OPENSSH "+"PRIVATE KEY-----"
	text := "before\n" + begin + "\nabc\ndef\n" + end + "\nafter"
	if got := PrivateKeyPattern.ReplaceAllString(text, "[K]"); got != "before\n[K]\nafter" {
		t.Fatalf("私钥块没整段换掉：%q", got)
	}
}

func TestPrivacyPatterns(t *testing.T) {
	check := func(pattern *regexp.Regexp, input, want string) {
		t.Helper()
		if got := pattern.ReplaceAllString(input, "[P]"); got != want {
			t.Errorf("%q → %q，期望 %q", input, got, want)
		}
	}
	check(EmailPattern, "联系 Someone.Name+tag@Example.co 谢谢", "联系 [P] 谢谢")
	check(PhonePattern, "电话13800000000", "电话[P]")
	check(PhonePattern, "call +86 138-0000-0000 now", "call [P] now")
	check(PhonePattern, "8613800000000", "[P]")
	check(PhonePattern, "138 0000 0000", "[P]")
	// 长串数字（消息 ID）里恰好有一段手机号形态的，不算手机号。
	check(PhonePattern, "message 7413800000000123", "message 7413800000000123")
	check(PhonePattern, "id_13800000000", "id_13800000000")
}
