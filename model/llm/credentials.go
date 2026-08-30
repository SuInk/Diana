// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// 凭据解析层。
//
// 以前 ProviderConfig.APIKey 是一个静态字符串，够用是因为 API Key 不会自己变。
// OAuth 不一样：access token 会过期，要拿 refresh token 换新的，而换的时机是
// 「下一次请求之前」——把它塞回一个静态字段，就得让保存配置的那条路去猜什么时候
// 该续期，猜错的表现是隔几小时机器人集体 401。
//
// 所以凭据改成每次请求前现问一次。API Key 的实现就是把同一个字符串还回去，
// 行为和以前完全一致；OAuth 的实现在 model/llmauth 里，负责判断过期并续期。

type CredentialKind string

const (
	CredentialKindAPIKey CredentialKind = "api_key"
	CredentialKindOAuth  CredentialKind = "oauth"
)

// Credential 是一次请求要用的凭据。
type Credential struct {
	Kind  CredentialKind
	Token string
	// TokenHeader 指定令牌写进哪个请求头，留空表示 Authorization。
	//
	// 「OAuth 令牌一定走 Authorization: Bearer」是个想当然的假设，而且是错的：
	// Anthropic 对 Bearer 里的 OAuth 令牌直接回 401「OAuth authentication is
	// currently not supported.」，它要的是把令牌放进 x-api-key。这类差异只有
	// 提供商自己说了算，所以由配置决定，不由这一层猜。
	TokenHeader string
	// TokenScheme 是令牌前面的鉴权方案名。留空时：写 Authorization 用 Bearer，
	// 写别的头则不加前缀——自定义鉴权头几乎都是直接放裸令牌。
	TokenScheme string
	// Headers 是这种凭据附带的请求头。OAuth 往往要求额外的标记头，
	// 所以由凭据自己带，不写死在适配器里。
	Headers map[string]string
	// ReplaceProviderAuth 让传输层摘掉 SDK 自己写上的鉴权头（放令牌的那个除外）。
	// 同时发两种鉴权头的请求会被一部分网关直接拒掉。
	ReplaceProviderAuth bool
}

// AuthHeader 返回令牌该写进哪个头、写成什么值。令牌为空时返回空名字。
func (c Credential) AuthHeader() (string, string) {
	token := strings.TrimSpace(c.Token)
	if token == "" {
		return "", ""
	}
	name := strings.TrimSpace(c.TokenHeader)
	if name == "" {
		name = "Authorization"
	}
	scheme := strings.TrimSpace(c.TokenScheme)
	if scheme == "" && strings.EqualFold(name, "Authorization") {
		scheme = "Bearer"
	}
	if scheme == "" {
		return name, token
	}
	return name, scheme + " " + token
}

// CredentialSource 在每次请求前解析出可用凭据。实现必须并发安全。
type CredentialSource interface {
	Credential(ctx context.Context) (Credential, error)
}

// StaticCredential 把一个固定字符串当成凭据，用于 API Key。
type StaticCredential struct {
	Key string
}

func (s StaticCredential) Credential(context.Context) (Credential, error) {
	return Credential{Kind: CredentialKindAPIKey, Token: s.Key}, nil
}

// CredentialSourceFunc 让调用方用一个函数充当凭据来源。
type CredentialSourceFunc func(ctx context.Context) (Credential, error)

func (f CredentialSourceFunc) Credential(ctx context.Context) (Credential, error) {
	return f(ctx)
}

// WithCredentialSource 注入凭据来源；不注入时沿用配置里的 API Key。
func WithCredentialSource(source CredentialSource) ClientOption {
	return func(opts *clientOptions) {
		opts.credentials = source
	}
}

// resolveCredential 取这次请求的凭据。
//
// 解析失败一律回退到配置里的 API Key，而不是让请求直接失败：OAuth 续期失败时，
// 如果这个配置档同时填了 API Key，能继续说话比集体哑掉好；真的两样都没有，
// 下游会照常报 ErrMissingAPIKey。
func (c clientOptions) resolveCredential(ctx context.Context, cfg ProviderConfig) Credential {
	if c.credentials == nil {
		return Credential{Kind: CredentialKindAPIKey, Token: cfg.APIKey}
	}
	credential, err := c.credentials.Credential(ctx)
	if err != nil || strings.TrimSpace(credential.Token) == "" {
		return Credential{Kind: CredentialKindAPIKey, Token: cfg.APIKey}
	}
	return credential
}

// cachedCredentialSource 把一次解析的结果按需缓存，避免同一轮对话里的多次请求
// 各自去打一遍续期接口。
type cachedCredentialSource struct {
	source CredentialSource

	mu     sync.Mutex
	cached Credential
	valid  bool
}

func newCachedCredentialSource(source CredentialSource) *cachedCredentialSource {
	return &cachedCredentialSource{source: source}
}

func (c *cachedCredentialSource) Credential(ctx context.Context) (Credential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid {
		return c.cached, nil
	}
	credential, err := c.source.Credential(ctx)
	if err != nil {
		return Credential{}, err
	}
	c.cached, c.valid = credential, true
	return credential, nil
}

// Invalidate 丢掉缓存，下一次请求重新解析。收到 401 时调用。
func (c *cachedCredentialSource) Invalidate() {
	c.mu.Lock()
	c.valid = false
	c.mu.Unlock()
}

// credentialTransport 在每个请求上换掉鉴权头。
//
// 三家 provider 用的是三套 SDK，都在创建客户端时就把 API Key 焊进去了。要让
// OAuth 的 access token 在过期后换新的，就得挑一个「每次请求都会经过」的位置——
// 那就是 RoundTripper。放在这里的好处是三家共用一份实现，适配器一行都不用改；
// 更重要的是续期发生在真正发请求的那一刻，而不是创建客户端的那一刻。
type credentialTransport struct {
	base   http.RoundTripper
	source CredentialSource
}

// providerAuthHeaders 是各家 SDK 自己会写上的鉴权头。用 OAuth 时必须把它们摘掉：
// 同时带着 API Key 和 OAuth 令牌的请求会被一部分网关直接拒掉，而那种 400
// 的报错信息通常不会说是因为鉴权头重复。放令牌的那个头当然要留下。
var providerAuthHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"}

func (t *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.source == nil {
		return base.RoundTrip(req)
	}
	credential, err := t.source.Credential(req.Context())
	if err != nil || credential.Kind != CredentialKindOAuth || strings.TrimSpace(credential.Token) == "" {
		// 解析不出 OAuth 凭据就原样放行，让 SDK 自己写的 API Key 继续生效。
		// 这条路径覆盖「配置档只填了 API Key」和「续期失败但仍配了 API Key」两种情况。
		return base.RoundTrip(req)
	}
	name, value := credential.AuthHeader()
	if name == "" {
		return base.RoundTrip(req)
	}
	// RoundTripper 不允许改传入的请求，按 http.RoundTripper 的约定先浅拷贝。
	cloned := req.Clone(req.Context())
	cloned.Header.Set(name, value)
	if credential.ReplaceProviderAuth {
		for _, header := range providerAuthHeaders {
			if strings.EqualFold(header, name) {
				continue
			}
			cloned.Header.Del(header)
		}
	}
	for name, value := range credential.Headers {
		cloned.Header.Set(name, value)
	}
	return base.RoundTrip(cloned)
}

// httpClientWithCredentials 在需要时把凭据注入包到 HTTP 客户端上。
func httpClientWithCredentials(client *http.Client, source CredentialSource) *http.Client {
	if source == nil {
		return client
	}
	if client == nil {
		client = http.DefaultClient
	}
	wrapped := *client
	wrapped.Transport = &credentialTransport{base: client.Transport, source: source}
	return &wrapped
}
