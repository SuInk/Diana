// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"

	"github.com/SuInk/diana/model/llmauth"
	"github.com/SuInk/diana/model/storage"
)

// LLMAuthStore 把 OAuth 登录态存进控制台自己的 sqlite。
//
// 单独一层而不是让 llmauth 直接依赖 storage：授权流程和存储实现没有关系，
// 单元测试里换成内存实现就行。
type LLMAuthStore struct {
	store *storage.SQLiteStore
}

func NewLLMAuthStore(store *storage.SQLiteStore) *LLMAuthStore {
	if store == nil {
		return nil
	}
	return &LLMAuthStore{store: store}
}

func (s *LLMAuthStore) LoadAuth(ctx context.Context) (llmauth.Document, error) {
	if s == nil || s.store == nil {
		return llmauth.Document{}, nil
	}
	return s.store.LoadLLMAuth(ctx)
}

func (s *LLMAuthStore) SaveAuth(ctx context.Context, document llmauth.Document) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.SaveLLMAuth(ctx, document)
}
