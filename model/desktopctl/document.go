// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"time"
)

// Token 是一把连接控制面的凭据。只存 SHA-256 哈希。
type Token struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`
	TokenHash  string    `json:"token_hash"`
	HelperID   string    `json:"helper_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// Document 是桌面控制的持久化状态。
type Document struct {
	EmergencyStop bool    `json:"emergency_stop"`
	Policy        Policy  `json:"policy"`
	Tokens        []Token `json:"tokens,omitempty"`
}

// Store 持久化桌面控制状态。为 nil 时 Registry 只留在内存。
type Store interface {
	LoadDesktopControl(ctx context.Context) (Document, bool, error)
	SaveDesktopControl(ctx context.Context, doc Document) error
}

// TokenInfo 是令牌在管理接口里的展示形态。
type TokenInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	HelperID   string     `json:"helper_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func tokenInfo(token Token) TokenInfo {
	info := TokenInfo{
		ID:        token.ID,
		Name:      token.Name,
		Prefix:    token.Prefix,
		HelperID:  token.HelperID,
		CreatedAt: token.CreatedAt,
	}
	if !token.LastUsedAt.IsZero() {
		lastUsed := token.LastUsedAt
		info.LastUsedAt = &lastUsed
	}
	return info
}
