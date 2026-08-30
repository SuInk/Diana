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
