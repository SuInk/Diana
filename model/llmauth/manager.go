// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llmauth

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Manager 管着「有哪些提供商」和「登录了哪些账号」，并把凭据接到 model/llm 上。

// Store 持久化提供商与令牌。令牌是明文凭据，实现必须和 API Key 同等对待。
type Store interface {
	LoadAuth(ctx context.Context) (Document, error)
	SaveAuth(ctx context.Context, doc Document) error
}

// Document 是落库的整份状态。
type Document struct {
	// CustomProviders 是用户在控制台自己加的提供商。内置的不落库，
	// 否则升级后旧数据里的地址会盖掉新版本的修正。
	CustomProviders []Provider `json:"custom_providers,omitempty"`
	Tokens          []Token    `json:"tokens,omitempty"`
}

type Manager struct {
	flow  *Flow
	store Store
	now   func() time.Time

	mu     sync.RWMutex
	custom []Provider
	tokens map[string]Token
}

func NewManager(store Store, client *http.Client) *Manager {
	return &Manager{
		flow:   NewFlow(client),
		store:  store,
		now:    time.Now,
		tokens: map[string]Token{},
	}
}

// Restore 从存储里读回状态。
func (m *Manager) Restore(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	doc, err := m.store.LoadAuth(ctx)
	if err != nil {
		return err
	}
	custom := make([]Provider, 0, len(doc.CustomProviders))
	for _, provider := range doc.CustomProviders {
		if provider.TokenRequestFormat == "" {
			// 这个字段是后加的，默认值是 form。但存下来的那些是在只发 JSON 的
			// 版本上配好并跑通的，把它们一起翻成 form 会让本来能用的配置突然
			// 换不到令牌。已经存在的按原样继续，新建的才按规范走。
			provider.TokenRequestFormat = TokenRequestJSON
		}
		normalized, err := provider.Normalize()
		if err != nil {
			// 一条坏数据不该让整页打不开，跳过并留痕。
			log.Printf("llmauth: 跳过无效的自定义提供商 %q: %v", provider.Key, err)
			continue
		}
		normalized.BuiltIn = false
		custom = append(custom, normalized)
	}
	tokens := make(map[string]Token, len(doc.Tokens))
	for _, token := range doc.Tokens {
		if key := strings.TrimSpace(token.ProviderKey); key != "" {
			tokens[key] = token
		}
	}
	m.mu.Lock()
	m.custom, m.tokens = custom, tokens
	m.mu.Unlock()
	return nil
}

func (m *Manager) persistLocked(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	doc := Document{CustomProviders: append([]Provider(nil), m.custom...)}
	for _, token := range m.tokens {
		doc.Tokens = append(doc.Tokens, token)
	}
	sort.Slice(doc.Tokens, func(i, j int) bool { return doc.Tokens[i].ProviderKey < doc.Tokens[j].ProviderKey })
	return m.store.SaveAuth(ctx, doc)
}

// Providers 返回内置加自定义的全部提供商，内置在前。
func (m *Manager) Providers() []Provider {
	out := builtinProviders()
	if m == nil {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	builtinKeys := make(map[string]bool, len(out))
	for _, provider := range out {
		builtinKeys[provider.Key] = true
	}
	for _, provider := range m.custom {
		// 自定义不允许顶掉内置：那会让「内置的地址是可信的」这个前提失效。
		if !builtinKeys[provider.Key] {
			out = append(out, provider)
		}
	}
	return out
}

// Provider 按标识取一个提供商。
func (m *Manager) Provider(key string) (Provider, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, provider := range m.Providers() {
		if provider.Key == key {
			return provider, true
		}
	}
	return Provider{}, false
}

// SaveCustomProvider 新增或更新一个自定义提供商。
func (m *Manager) SaveCustomProvider(ctx context.Context, provider Provider) (Provider, error) {
	normalized, err := provider.Normalize()
	if err != nil {
		return Provider{}, err
	}
	normalized.BuiltIn = false
	for _, builtin := range builtinProviders() {
		if builtin.Key == normalized.Key {
			return Provider{}, fmt.Errorf("llmauth: %q 是内置提供商，不能覆盖", normalized.Key)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	replaced := false
	for index := range m.custom {
		if m.custom[index].Key == normalized.Key {
			// 改地址等于换了一家服务，旧令牌不能跟着走。
			if m.custom[index].AuthorizeURL != normalized.AuthorizeURL || m.custom[index].TokenURL != normalized.TokenURL || m.custom[index].ClientID != normalized.ClientID {
				delete(m.tokens, normalized.Key)
			}
			m.custom[index] = normalized
			replaced = true
			break
		}
	}
	if !replaced {
		m.custom = append(m.custom, normalized)
	}
	if err := m.persistLocked(ctx); err != nil {
		return Provider{}, err
	}
	return normalized, nil
}

// DeleteCustomProvider 删掉一个自定义提供商，连同它的令牌。
func (m *Manager) DeleteCustomProvider(ctx context.Context, key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := make([]Provider, 0, len(m.custom))
	found := false
	for _, provider := range m.custom {
		if provider.Key == key {
			found = true
			continue
		}
		filtered = append(filtered, provider)
	}
	if !found {
		return fmt.Errorf("llmauth: 没有找到提供商 %q", key)
	}
	m.custom = filtered
	delete(m.tokens, key)
	return m.persistLocked(ctx)
}

// StartLogin 发起一次登录，返回要让用户打开的授权地址。
func (m *Manager) StartLogin(key string) (PendingLogin, error) {
	provider, ok := m.Provider(key)
	if !ok {
		return PendingLogin{}, fmt.Errorf("llmauth: 没有找到提供商 %q", key)
	}
	return m.flow.Start(provider)
}

// CompleteLogin 用粘回来的回调地址或授权码换取令牌并落库。
func (m *Manager) CompleteLogin(ctx context.Context, key, loginID, pasted string) (Token, error) {
	provider, ok := m.Provider(key)
	if !ok {
		return Token{}, fmt.Errorf("llmauth: 没有找到提供商 %q", key)
	}
	token, err := m.flow.Complete(ctx, provider, loginID, pasted)
	if err != nil {
		return Token{}, err
	}
	m.mu.Lock()
	m.tokens[provider.Key] = token
	err = m.persistLocked(ctx)
	m.mu.Unlock()
	if err != nil {
		return Token{}, err
	}
	return token.Redacted(), nil
}

// CancelLogin 放弃一次没完成的登录。
func (m *Manager) CancelLogin(id string) { m.flow.Cancel(id) }

// Logout 清掉一家的登录态。
func (m *Manager) Logout(ctx context.Context, key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[key]; !ok {
		return fmt.Errorf("llmauth: %q 当前没有登录", key)
	}
	delete(m.tokens, key)
	return m.persistLocked(ctx)
}

// Status 是控制台看到的一家提供商的登录状态。令牌本身永远不出现在这里。
type Status struct {
	Provider   Provider   `json:"provider"`
	LoggedIn   bool       `json:"logged_in"`
	Account    string     `json:"account,omitempty"`
	ObtainedAt *time.Time `json:"obtained_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// Expired 表示当前这把令牌已经不能用了；Refreshable 表示还能自己救回来。
	Expired     bool   `json:"expired,omitempty"`
	Refreshable bool   `json:"refreshable,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// Statuses 返回全部提供商的登录状态。
func (m *Manager) Statuses() []Status {
	providers := m.Providers()
	out := make([]Status, 0, len(providers))
	now := m.now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, provider := range providers {
		status := Status{Provider: provider.redactedSecrets()}
		if token, ok := m.tokens[provider.Key]; ok && strings.TrimSpace(token.AccessToken) != "" {
			status.LoggedIn = true
			status.Account = token.Account
			status.Scope = token.Scope
			status.Expired = token.Expired(now)
			status.Refreshable = token.Refreshable()
			obtained := token.ObtainedAt
			status.ObtainedAt = &obtained
			if !token.ExpiresAt.IsZero() {
				expires := token.ExpiresAt
				status.ExpiresAt = &expires
			}
		}
		out = append(out, status)
	}
	return out
}

// redactedSecrets 抹掉提供商里的机密，只留「是否配置过」由调用方自行判断。
func (p Provider) redactedSecrets() Provider {
	if p.ClientSecret != "" {
		p.ClientSecret = "***"
	}
	return p
}

// HasToken 判断一家是否已登录，用于决定要不要给这个配置档接 OAuth 凭据。
func (m *Manager) HasToken(key string) bool {
	if m == nil {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	m.mu.RLock()
	defer m.mu.RUnlock()
	token, ok := m.tokens[key]
	return ok && strings.TrimSpace(token.AccessToken) != ""
}

// Credential 取一家当前可用的凭据，过期就地续期。
//
// 续期在这里做而不是靠后台定时任务：定时任务要么跑得太勤白打接口，要么正好
// 错过一次过期。「用之前看一眼」既准确，代价也只是一次比较。
func (m *Manager) Credential(ctx context.Context, key string) (llm.Credential, error) {
	provider, ok := m.Provider(key)
	if !ok {
		return llm.Credential{}, fmt.Errorf("llmauth: 没有找到提供商 %q", key)
	}
	m.mu.RLock()
	token, exists := m.tokens[provider.Key]
	m.mu.RUnlock()
	if !exists || strings.TrimSpace(token.AccessToken) == "" {
		return llm.Credential{}, fmt.Errorf("llmauth: %s 还没有登录", provider.Label)
	}
	now := m.now()
	if !token.Valid(now) {
		if !token.Refreshable() {
			return llm.Credential{}, fmt.Errorf("llmauth: %s 的登录已过期，需要重新登录", provider.Label)
		}
		refreshed, err := m.flow.Refresh(ctx, provider, token)
		if err != nil {
			return llm.Credential{}, fmt.Errorf("llmauth: %s 续期失败: %w", provider.Label, err)
		}
		m.mu.Lock()
		m.tokens[provider.Key] = refreshed
		persistErr := m.persistLocked(ctx)
		m.mu.Unlock()
		if persistErr != nil {
			// 存不下来不代表这把令牌不能用，但下次重启会退回旧的，值得留痕。
			log.Printf("llmauth: 保存 %s 续期结果失败: %v", provider.Label, persistErr)
		}
		token = refreshed
	}
	return llm.Credential{
		Kind:                llm.CredentialKindOAuth,
		Token:               token.AccessToken,
		TokenHeader:         provider.TokenHeader,
		TokenScheme:         provider.TokenScheme,
		Headers:             provider.TokenHeaders,
		ReplaceProviderAuth: true,
	}, nil
}

// CredentialSource 返回可以直接交给 model/llm 的凭据来源。
func (m *Manager) CredentialSource(key string) llm.CredentialSource {
	return llm.CredentialSourceFunc(func(ctx context.Context) (llm.Credential, error) {
		return m.Credential(ctx, key)
	})
}
