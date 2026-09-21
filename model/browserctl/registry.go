// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// tokenPlaintextPrefix 让令牌在日志或配置里露出来时一眼能认出来源。
	tokenPlaintextPrefix = "dianabx_"
	tokenRandomBytes     = 32
	// tokenPrefixRunes 是列表里展示的明文前缀长度，够辨认、不够重放。
	tokenPrefixRunes = 16
	tokenNameMaxLen  = 64
	// extensionIDMaxLen 挡住把一整段 UA 当扩展 ID 报上来。
	extensionIDMaxLen = 128
	// tokenLastUsedFlushInterval 决定最近使用时间攒多久写一次盘。
	tokenLastUsedFlushInterval = 5 * time.Minute
)

var (
	// ErrTokenNameInvalid 说明令牌名称不合规。
	ErrTokenNameInvalid = errors.New("令牌名称需为 1-64 个字符，且不能包含控制字符")
	// ErrTokenNotFound 说明要吊销的令牌不存在。
	ErrTokenNotFound = errors.New("令牌不存在")
	// ErrExtensionIDInvalid 说明扩展没有报出可用的扩展 ID。
	ErrExtensionIDInvalid = errors.New("扩展 ID 缺失或不合法")
	// ErrExtensionMismatch 说明令牌已钉定在另一个扩展上。
	ErrExtensionMismatch = errors.New("令牌已绑定到其他扩展")
)

// Registry 管理浏览器控制的策略与令牌，并负责落盘。
// 它不碰连接，也不下发指令，那是 Hub 的事；这里只回答「这把令牌能不能连」
// 和「现在的授权边界是什么」。
type Registry struct {
	store Store

	mu     sync.RWMutex
	policy Policy
	tokens map[string]Token // token 哈希 -> 令牌
}

// NewRegistry 创建注册表并从存储加载状态。存储读失败时按空状态启动：
// 空状态是「全关」，不会因为读不出配置就放开权限。
func NewRegistry(ctx context.Context, store Store) *Registry {
	r := &Registry{store: store, policy: Policy{}.WithDefaults(), tokens: map[string]Token{}}
	if store == nil {
		return r
	}
	doc, ok, err := store.LoadBrowserControl(ctx)
	if err != nil || !ok {
		return r
	}
	r.policy = doc.Policy.WithDefaults()
	for _, token := range doc.Tokens {
		token.TokenHash = strings.TrimSpace(token.TokenHash)
		if token.TokenHash == "" {
			continue
		}
		r.tokens[token.TokenHash] = token
	}
	return r
}

// Policy 返回当前策略副本。
func (r *Registry) Policy() Policy {
	if r == nil {
		return Policy{}.WithDefaults()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy.clone()
}

// SetPolicy 覆盖策略并落盘。返回规范化后的结果，调用方据此回显。
func (r *Registry) SetPolicy(ctx context.Context, policy Policy) (Policy, error) {
	if r == nil {
		return Policy{}, errors.New("browser control registry 未初始化")
	}
	policy = policy.WithDefaults()
	r.mu.Lock()
	previous := r.policy
	r.policy = policy
	r.mu.Unlock()
	if err := r.persist(ctx); err != nil {
		// 落盘失败就回滚内存态：重启即失效的「幽灵授权」比报错更难查。
		r.mu.Lock()
		r.policy = previous
		r.mu.Unlock()
		return Policy{}, err
	}
	return policy.clone(), nil
}

// CreateToken 生成一把新令牌；返回值里的明文是唯一一次能拿到的机会。
func (r *Registry) CreateToken(ctx context.Context, name string) (TokenInfo, string, error) {
	if r == nil {
		return TokenInfo{}, "", errors.New("browser control registry 未初始化")
	}
	name = strings.TrimSpace(name)
	if err := validateTokenName(name); err != nil {
		return TokenInfo{}, "", err
	}
	raw := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return TokenInfo{}, "", err
	}
	plaintext := tokenPlaintextPrefix + hex.EncodeToString(raw)
	tokenHash := HashToken(plaintext)
	record := Token{
		ID:        tokenHash[:16],
		Name:      name,
		Prefix:    plaintext[:tokenPrefixRunes],
		TokenHash: tokenHash,
		CreatedAt: time.Now(),
	}
	r.mu.Lock()
	r.tokens[tokenHash] = record
	r.mu.Unlock()
	if err := r.persist(ctx); err != nil {
		r.mu.Lock()
		delete(r.tokens, tokenHash)
		r.mu.Unlock()
		return TokenInfo{}, "", err
	}
	return tokenInfo(record), plaintext, nil
}

// ListTokens 返回全部令牌，按创建时间倒序。
func (r *Registry) ListTokens() []TokenInfo {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	items := make([]TokenInfo, 0, len(r.tokens))
	for _, token := range r.tokens {
		items = append(items, tokenInfo(token))
	}
	r.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items
}

// RevokeToken 吊销一把令牌；吊销即删除，没有恢复。
func (r *Registry) RevokeToken(ctx context.Context, id string) (TokenInfo, error) {
	if r == nil {
		return TokenInfo{}, ErrTokenNotFound
	}
	id = strings.TrimSpace(id)
	r.mu.Lock()
	foundHash := ""
	var found Token
	for tokenHash, token := range r.tokens {
		if token.ID == id {
			foundHash, found = tokenHash, token
			break
		}
	}
	if foundHash != "" {
		delete(r.tokens, foundHash)
	}
	r.mu.Unlock()
	if foundHash == "" {
		return TokenInfo{}, ErrTokenNotFound
	}
	if err := r.persist(ctx); err != nil {
		return TokenInfo{}, err
	}
	return tokenInfo(found), nil
}

// Authenticate 校验令牌明文并把它钉在扩展 ID 上。
//
// 第一次用某把令牌握手时记下扩展 ID；之后必须是同一个扩展。
// 想换扩展就吊销重发一把，这比「令牌对了就放进来」多挡一层。
func (r *Registry) Authenticate(ctx context.Context, plaintext, extensionID string) (TokenInfo, error) {
	if r == nil {
		return TokenInfo{}, ErrTokenNotFound
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return TokenInfo{}, ErrTokenNotFound
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" || len(extensionID) > extensionIDMaxLen {
		return TokenInfo{}, ErrExtensionIDInvalid
	}
	tokenHash := HashToken(plaintext)
	now := time.Now()
	r.mu.Lock()
	token, ok := r.tokens[tokenHash]
	if !ok {
		r.mu.Unlock()
		return TokenInfo{}, ErrTokenNotFound
	}
	if token.ExtensionID != "" && token.ExtensionID != extensionID {
		r.mu.Unlock()
		return TokenInfo{}, ErrExtensionMismatch
	}
	pinned := token.ExtensionID == ""
	if pinned {
		token.ExtensionID = extensionID
	}
	// 最近使用时间精度要求不高，攒一会儿写一次，别让每次重连都落一次盘。
	touch := token.LastUsedAt.IsZero() || now.Sub(token.LastUsedAt) >= tokenLastUsedFlushInterval
	if touch {
		token.LastUsedAt = now
	}
	r.tokens[tokenHash] = token
	r.mu.Unlock()
	if pinned || touch {
		// 钉定失败不该把已经通过的握手判成失败，但下次重启会重新钉，
		// 所以只记错误不阻断；调用方看日志即可。
		_ = r.persist(ctx)
	}
	return tokenInfo(token), nil
}

func (r *Registry) persist(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	r.mu.RLock()
	doc := Document{Policy: r.policy.clone(), Tokens: make([]Token, 0, len(r.tokens))}
	for tokenHash, token := range r.tokens {
		token.TokenHash = tokenHash
		doc.Tokens = append(doc.Tokens, token)
	}
	r.mu.RUnlock()
	sort.Slice(doc.Tokens, func(i, j int) bool { return doc.Tokens[i].ID < doc.Tokens[j].ID })
	return r.store.SaveBrowserControl(ctx, doc)
}

// HashToken 返回令牌明文的 SHA-256 十六进制，存储与比对都只用它。
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func validateTokenName(name string) error {
	length := len([]rune(name))
	if length < 1 || length > tokenNameMaxLen {
		return ErrTokenNameInvalid
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return ErrTokenNameInvalid
		}
	}
	return nil
}

func (p Policy) clone() Policy {
	p.AllowedOrigins = append([]string(nil), p.AllowedOrigins...)
	p.AllowedHosts = append([]string(nil), p.AllowedHosts...)
	p.DeniedHosts = append([]string(nil), p.DeniedHosts...)
	return p
}
