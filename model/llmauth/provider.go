// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llmauth

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// 一个 OAuth 提供商长什么样。
//
// 做成数据而不是每家一段代码，是因为这类集成的差异几乎全在「几个地址、一个
// client ID、一串 scope」上，流程本身是同一套 RFC。配置驱动还有个实际好处：
// 用户要接自己的企业网关或某家我们没内置的服务，在控制台填几个框就行，
// 不用等这边发版。

// Provider 描述一家可以用 OAuth 登录的模型服务。
type Provider struct {
	// Key 是稳定标识，凭据按它归档。
	Key string `json:"key"`
	// Label 是控制台上显示的名字。
	Label string `json:"label"`
	// AuthorizeURL / TokenURL 是授权页和换取令牌的接口。
	AuthorizeURL string `json:"authorize_url"`
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
	// ClientSecret 对公共客户端（PKCE）应当留空。
	ClientSecret string `json:"client_secret,omitempty"`
	// RedirectURI 必须和提供商那边登记的完全一致。
	RedirectURI string   `json:"redirect_uri"`
	Scopes      []string `json:"scopes,omitempty"`
	// UsePKCE 关掉后走传统的 client_secret 交换，只适合自建网关。
	// 默认开启：公共客户端不带 PKCE 就等于把授权码裸奔在重定向里。
	UsePKCE bool `json:"use_pkce"`
	// TokenHeaders 是拿这家的令牌发模型请求时要额外带的头。
	TokenHeaders map[string]string `json:"token_headers,omitempty"`
	// TokenHeader / TokenScheme 决定令牌本身写进哪个头、加不加前缀。
	//
	// 留空是 Authorization + Bearer，绝大多数提供商都是这样。但不能写死：
	// Anthropic 收到 Authorization 里的 OAuth 令牌会回 401「OAuth authentication
	// is currently not supported.」，它要求令牌放在 x-api-key 里、且不带前缀。
	// 这种事只有各家自己说了算，所以做成配置项。
	TokenHeader string `json:"token_header,omitempty"`
	TokenScheme string `json:"token_scheme,omitempty"`
	// BuiltIn 标记内置提供商，控制台里不允许删除或改地址。
	BuiltIn bool `json:"built_in,omitempty"`
	// Notes 是控制台上给用户看的一句说明。
	Notes string `json:"notes,omitempty"`
}

// builtinProviders 是随发行版附带的提供商。
//
// 只内置那些「厂商明确为第三方应用提供了授权流程」的服务。拿订阅账号登录态的
// 那一类（需要冒用第一方客户端的 client ID）不在此列：那超出订阅本身的授权范围，
// 要不要用是部署者自己的判断，可以在控制台自定义里填，但不该由我们预置。
func builtinProviders() []Provider {
	return []Provider{
		{
			Key:          "openrouter",
			Label:        "OpenRouter",
			AuthorizeURL: "https://openrouter.ai/auth",
			TokenURL:     "https://openrouter.ai/api/v1/auth/keys",
			RedirectURI:  "",
			UsePKCE:      true,
			BuiltIn:      true,
			Notes:        "OpenRouter 的 PKCE 授权本就是给第三方应用用的，换到的是一把归你所有、可随时吊销的 Key。",
		},
	}
}

// Normalize 补默认值并做基本校验。
func (p Provider) Normalize() (Provider, error) {
	p.Key = strings.ToLower(strings.TrimSpace(p.Key))
	p.Label = strings.TrimSpace(p.Label)
	p.AuthorizeURL = strings.TrimSpace(p.AuthorizeURL)
	p.TokenURL = strings.TrimSpace(p.TokenURL)
	p.ClientID = strings.TrimSpace(p.ClientID)
	p.ClientSecret = strings.TrimSpace(p.ClientSecret)
	p.RedirectURI = strings.TrimSpace(p.RedirectURI)
	p.TokenHeader = strings.TrimSpace(p.TokenHeader)
	p.TokenScheme = strings.TrimSpace(p.TokenScheme)
	p.Notes = strings.TrimSpace(p.Notes)
	if p.Key == "" {
		return Provider{}, fmt.Errorf("llmauth: 提供商标识不能为空")
	}
	if p.Label == "" {
		p.Label = p.Key
	}
	if err := requireHTTPSURL("授权地址", p.AuthorizeURL); err != nil {
		return Provider{}, err
	}
	if err := requireHTTPSURL("令牌地址", p.TokenURL); err != nil {
		return Provider{}, err
	}
	if p.RedirectURI != "" {
		if _, err := url.Parse(p.RedirectURI); err != nil {
			return Provider{}, fmt.Errorf("llmauth: 回调地址无效: %w", err)
		}
	}
	if err := requireHeaderName("令牌请求头", p.TokenHeader); err != nil {
		return Provider{}, err
	}
	if strings.ContainsAny(p.TokenScheme, " \t\r\n") {
		return Provider{}, fmt.Errorf("llmauth: 令牌前缀不能含空白字符")
	}
	if len(p.TokenHeaders) > 0 {
		headers := make(map[string]string, len(p.TokenHeaders))
		for name, value := range p.TokenHeaders {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if err := requireHeaderName("附加请求头", name); err != nil {
				return Provider{}, err
			}
			value = strings.TrimSpace(value)
			if strings.ContainsAny(value, "\r\n") {
				return Provider{}, fmt.Errorf("llmauth: 附加请求头 %q 的值不能含换行", name)
			}
			headers[name] = value
		}
		p.TokenHeaders = headers
	}
	scopes := make([]string, 0, len(p.Scopes))
	for _, scope := range p.Scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	p.Scopes = scopes
	if p.ClientSecret == "" {
		// 没有 secret 就一定是公共客户端，公共客户端必须带 PKCE。
		p.UsePKCE = true
	}
	return p, nil
}

// requireHTTPSURL 拒绝明文和畸形地址。
//
// 授权码和令牌都会经过这两个地址，走 http 等于把它们明文发出去；而这个地址是
// 用户在控制台填的，填错的代价不该由用户在事后自己发现。
//
// 回环地址是例外：自建网关跑在 127.0.0.1 是常态，那段流量不出本机，
// 逼它先配一张证书只是把人挡在门外。
func requireHTTPSURL(label, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("llmauth: %s不能为空", label)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("llmauth: %s无效: %w", label, err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("llmauth: %s缺少域名", label)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("llmauth: %s必须是 https（本机回环地址除外）", label)
}

// requireHeaderName 校验用户填的请求头名字。留空表示用默认值，不算错。
//
// 头名字直接拼进请求里，含空格或冒号的值会把这一行撕成两行——那是请求头注入，
// 而这一行里带的正是令牌。
func requireHeaderName(label, name string) error {
	if name == "" {
		return nil
	}
	for _, r := range name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune(":()<>@,;\\\"/[]?={}", r) {
			return fmt.Errorf("llmauth: %s名不合法: %q", label, name)
		}
	}
	return nil
}

// isLoopbackHost 判断主机名是不是本机回环。
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "localhost" {
		return true
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.IsLoopback()
	}
	return false
}
