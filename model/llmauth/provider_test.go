// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llmauth

import "testing"

// 令牌落点是配置项，保存时要留住。
func TestNormalizeKeepsTheTokenPlacement(t *testing.T) {
	provider, err := Provider{
		Key:          "example",
		AuthorizeURL: "https://example.invalid/authorize",
		TokenURL:     "https://example.invalid/token",
		TokenHeader:  "  x-api-key  ",
		TokenScheme:  "  ",
		TokenHeaders: map[string]string{"  anthropic-beta  ": "  oauth-2025-04-20  ", "   ": "dropped"},
	}.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if provider.TokenHeader != "x-api-key" || provider.TokenScheme != "" {
		t.Fatalf("token placement = %q / %q", provider.TokenHeader, provider.TokenScheme)
	}
	if got := provider.TokenHeaders["anthropic-beta"]; got != "oauth-2025-04-20" {
		t.Fatalf("token headers = %#v", provider.TokenHeaders)
	}
	if len(provider.TokenHeaders) != 1 {
		t.Fatalf("the empty header name survived: %#v", provider.TokenHeaders)
	}
}

// 头名字和头值直接拼进请求里，含分隔符或换行的值等于请求头注入——而这一行带的
// 正是令牌，所以在保存这一步就拦下。
func TestNormalizeRejectsHeaderInjection(t *testing.T) {
	base := Provider{
		Key:          "example",
		AuthorizeURL: "https://example.invalid/authorize",
		TokenURL:     "https://example.invalid/token",
	}
	cases := map[string]func(Provider) Provider{
		"头名带空格":  func(p Provider) Provider { p.TokenHeader = "X Api Key"; return p },
		"头名带冒号":  func(p Provider) Provider { p.TokenHeader = "X-Api-Key: leak"; return p },
		"头名带换行":  func(p Provider) Provider { p.TokenHeader = "X-Api-Key\r\nX-Evil"; return p },
		"前缀带空白":  func(p Provider) Provider { p.TokenScheme = "Bearer x"; return p },
		"附加头名非法": func(p Provider) Provider { p.TokenHeaders = map[string]string{"A B": "c"}; return p },
		"附加头值换行": func(p Provider) Provider { p.TokenHeaders = map[string]string{"A": "b\r\nX-Evil: c"}; return p },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := mutate(base).Normalize(); err == nil {
				t.Fatal("Normalize() accepted a header that could split the request")
			}
		})
	}
}

// 换令牌的请求体格式：默认是 RFC 6749 规定的 form，只认 form 和 json。
func TestNormalizeTokenRequestFormat(t *testing.T) {
	base := Provider{
		Key:          "example",
		AuthorizeURL: "https://example.invalid/authorize",
		TokenURL:     "https://example.invalid/token",
	}
	for _, tc := range []struct {
		in   TokenRequestFormat
		want TokenRequestFormat
	}{
		{"", TokenRequestForm},
		{"  JSON ", TokenRequestJSON},
		{"form", TokenRequestForm},
	} {
		provider := base
		provider.TokenRequestFormat = tc.in
		normalized, err := provider.Normalize()
		if err != nil {
			t.Fatalf("Normalize(%q) error = %v", tc.in, err)
		}
		if normalized.TokenRequestFormat != tc.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tc.in, normalized.TokenRequestFormat, tc.want)
		}
	}
	provider := base
	provider.TokenRequestFormat = "xml"
	if _, err := provider.Normalize(); err == nil {
		t.Fatal("Normalize() accepted an unknown token request format")
	}
}

// 内置的 OpenRouter 走的不是标准令牌端点，它的文档写死了 application/json。
func TestOpenRouterKeepsJSONTokenRequests(t *testing.T) {
	for _, provider := range builtinProviders() {
		if provider.Key != "openrouter" {
			continue
		}
		normalized, err := provider.Normalize()
		if err != nil {
			t.Fatalf("Normalize() error = %v", err)
		}
		if normalized.TokenRequestFormat != TokenRequestJSON {
			t.Fatalf("OpenRouter token request format = %q, want json", normalized.TokenRequestFormat)
		}
		return
	}
	t.Fatal("OpenRouter is no longer a built-in provider")
}

// 请求体按声明的格式打包，form 是默认。
func TestEncodeTokenRequest(t *testing.T) {
	payload := map[string]string{"grant_type": "authorization_code", "code": "a b&c"}

	body, contentType, err := encodeTokenRequest(TokenRequestForm, payload)
	if err != nil {
		t.Fatalf("encodeTokenRequest() error = %v", err)
	}
	if contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("form content type = %q", contentType)
	}
	// url.Values.Encode 按键名排序，所以这里可以整串比对，顺带确认转义。
	if body != "code=a+b%26c&grant_type=authorization_code" {
		t.Fatalf("form body = %q", body)
	}

	body, contentType, err = encodeTokenRequest(TokenRequestJSON, payload)
	if err != nil {
		t.Fatalf("encodeTokenRequest() error = %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("json content type = %q", contentType)
	}
	// encoding/json 会把 & 转义成 \u0026，这里照实比对，免得下次有人「修好」它。
	if body != `{"code":"a b\u0026c","grant_type":"authorization_code"}` {
		t.Fatalf("json body = %q", body)
	}
}
