// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"context"
	"time"
)

// Token 是一把连接控制面的凭据。只存 SHA-256 哈希，明文只在创建那一刻返回一次。
type Token struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// TokenHash 是明文的 SHA-256 十六进制。
	TokenHash string `json:"token_hash"`
	// ExtensionID 在首次成功握手时钉定。之后另一个扩展拿同一把令牌连过来会被拒：
	// 令牌抄走了也得在同一个扩展里用，不能随手换个浏览器接管用户的会话。
	ExtensionID string    `json:"extension_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
}

// Document 是浏览器控制的持久化状态：一份策略加一批令牌。
type Document struct {
	Policy Policy  `json:"policy"`
	Tokens []Token `json:"tokens,omitempty"`
}

// Store 持久化浏览器控制状态。
type Store interface {
	LoadBrowserControl(ctx context.Context) (Document, bool, error)
	SaveBrowserControl(ctx context.Context, doc Document) error
}

// TokenInfo 是令牌在管理接口里的展示形态，永远不含哈希。
type TokenInfo struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	ExtensionID string     `json:"extension_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

func tokenInfo(token Token) TokenInfo {
	info := TokenInfo{
		ID:          token.ID,
		Name:        token.Name,
		Prefix:      token.Prefix,
		ExtensionID: token.ExtensionID,
		CreatedAt:   token.CreatedAt,
	}
	if !token.LastUsedAt.IsZero() {
		lastUsed := token.LastUsedAt
		info.LastUsedAt = &lastUsed
	}
	return info
}
