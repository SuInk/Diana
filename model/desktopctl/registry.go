// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

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
	tokenPlaintextPrefix       = "dianadx_"
	tokenRandomBytes           = 32
	tokenPrefixRunes           = 16
	tokenNameMaxLen            = 64
	helperIDMaxLen             = 128
	tokenLastUsedFlushInterval = 5 * time.Minute
)

var (
	ErrTokenNameInvalid = errors.New("令牌名称需为 1-64 个字符，且不能包含控制字符")
	ErrTokenNotFound    = errors.New("令牌不存在")
	ErrHelperIDInvalid  = errors.New("执行器 ID 缺失或不合法")
	ErrHelperMismatch   = errors.New("令牌已绑定到其他执行器")
)

// Registry 管理桌面控制的策略与令牌。
type Registry struct {
	store Store

	mu     sync.RWMutex
	policy Policy
	tokens map[string]Token
}

// NewRegistry 创建注册表。store 为 nil 或读失败时按「全关」空状态启动。
func NewRegistry(ctx context.Context, store Store) *Registry {
	r := &Registry{store: store, policy: Policy{}.WithDefaults(), tokens: map[string]Token{}}
	if store == nil {
		return r
	}
	doc, ok, err := store.LoadDesktopControl(ctx)
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

// SetPolicy 覆盖策略并落盘。
func (r *Registry) SetPolicy(ctx context.Context, policy Policy) (Policy, error) {
	if r == nil {
		return Policy{}, errors.New("desktop control registry 未初始化")
	}
	policy = policy.WithDefaults()
	r.mu.Lock()
	previous := r.policy
	r.policy = policy
	r.mu.Unlock()
	if err := r.persist(ctx); err != nil {
		r.mu.Lock()
		r.policy = previous
		r.mu.Unlock()
		return Policy{}, err
	}
	return policy.clone(), nil
}

// CreateToken 生成一把新令牌。
func (r *Registry) CreateToken(ctx context.Context, name string) (TokenInfo, string, error) {
	if r == nil {
		return TokenInfo{}, "", errors.New("desktop control registry 未初始化")
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

// ListTokens 返回全部令牌。
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

// RevokeToken 吊销一把令牌。
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

// Authenticate 校验令牌并钉在执行器 ID 上。
func (r *Registry) Authenticate(ctx context.Context, plaintext, helperID string) (TokenInfo, error) {
	if r == nil {
		return TokenInfo{}, ErrTokenNotFound
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return TokenInfo{}, ErrTokenNotFound
	}
	helperID = strings.TrimSpace(helperID)
	if helperID == "" || len(helperID) > helperIDMaxLen {
		return TokenInfo{}, ErrHelperIDInvalid
	}
	tokenHash := HashToken(plaintext)
	now := time.Now()
	r.mu.Lock()
	token, ok := r.tokens[tokenHash]
	if !ok {
		r.mu.Unlock()
		return TokenInfo{}, ErrTokenNotFound
	}
	if token.HelperID != "" && token.HelperID != helperID {
		r.mu.Unlock()
		return TokenInfo{}, ErrHelperMismatch
	}
	pinned := token.HelperID == ""
	if pinned {
		token.HelperID = helperID
	}
	touch := token.LastUsedAt.IsZero() || now.Sub(token.LastUsedAt) >= tokenLastUsedFlushInterval
	if touch {
		token.LastUsedAt = now
	}
	r.tokens[tokenHash] = token
	r.mu.Unlock()
	if pinned || touch {
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
	return r.store.SaveDesktopControl(ctx, doc)
}

// HashToken 返回令牌明文的 SHA-256 十六进制。
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
