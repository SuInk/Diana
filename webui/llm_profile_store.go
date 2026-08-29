// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"sync"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"
)

type LLMProfileStore interface {
	Current() llm.ProviderConfig
	Profiles() llm.ProfileSet
	SaveProfiles(llm.ProfileSet) error
}

type MemoryLLMProfileStore struct {
	mu   sync.RWMutex
	data llm.ProfileSet
}

// NewMemoryLLMProfileStore 创建内存版提供商配置集存储。
func NewMemoryLLMProfileStore(cfg llm.ProviderConfig) *MemoryLLMProfileStore {
	return &MemoryLLMProfileStore{data: llm.NewProfileSet(cfg)}
}

// Current 返回内存存储中的当前提供商配置。
func (s *MemoryLLMProfileStore) Current() llm.ProviderConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if profile, ok := s.data.FirstProfile(); ok {
		return profile.Config.WithDefaults()
	}
	return llm.ProviderConfig{}
}

// Profiles 返回内存存储中的提供商配置集。
func (s *MemoryLLMProfileStore) Profiles() llm.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 更新内存中的提供商配置集。
func (s *MemoryLLMProfileStore) SaveProfiles(set llm.ProfileSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = set.WithDefaults()
	return nil
}

type PersistentLLMProfileStore struct {
	mu       sync.RWMutex
	data     llm.ProfileSet
	registry llm.ProviderRegistryDocument
	store    *storage.SQLiteStore
	ctx      context.Context
}

// NewPersistentLLMProfileStore 创建 SQLite 持久化版提供商配置集存储。
func NewPersistentLLMProfileStore(ctx context.Context, store *storage.SQLiteStore, fallback llm.ProviderConfig) (*PersistentLLMProfileStore, error) {
	data := llm.NewProfileSet(fallback)
	if saved, ok, err := store.LoadLLMProfiles(ctx); err != nil {
		return nil, err
	} else if ok && len(saved.Profiles) > 0 {
		data = saved.WithDefaults()
	}
	data = data.WithDefaults()
	registry, registryOK, err := store.LoadLLMProviderRegistry(ctx)
	if err != nil {
		return nil, err
	}
	if !registryOK || registry.Version == 0 {
		migrated, _, migrationErr := llm.NewProviderRegistryFromProfiles(data)
		if migrationErr != nil {
			return nil, migrationErr
		}
		registry = migrated.Document()
		if err := store.SaveLLMProviderRegistry(ctx, registry); err != nil {
			return nil, err
		}
	}
	return &PersistentLLMProfileStore{
		data:     data,
		registry: registry,
		store:    store,
		ctx:      ctx,
	}, nil
}

func (s *PersistentLLMProfileStore) ProviderRegistry() (*llm.ProviderRegistry, error) {
	s.mu.RLock()
	document := s.registry
	s.mu.RUnlock()
	return llm.RegistryFromDocument(document)
}

// Current 返回持久化存储中的当前提供商配置。
func (s *PersistentLLMProfileStore) Current() llm.ProviderConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if profile, ok := s.data.FirstProfile(); ok {
		return profile.Config.WithDefaults()
	}
	return llm.ProviderConfig{}
}

// Profiles 返回持久化存储中的提供商配置集。
func (s *PersistentLLMProfileStore) Profiles() llm.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 保存提供商配置集。
// 落库失败必须往上抛，否则接口回 200、前端提示保存成功，重启后配置又是旧的。
func (s *PersistentLLMProfileStore) SaveProfiles(set llm.ProfileSet) error {
	set = set.WithDefaults()
	s.mu.Lock()
	s.data = set
	s.mu.Unlock()
	if s.store == nil {
		return nil
	}
	// 落库前剥掉 WithDefaults 派生出来的上下文窗口，只保存用户填的真实值。
	// 否则当前兜底值会被写死进数据库，日后改兜底或改推断表都追不回来。
	if err := s.store.SaveLLMProfiles(s.ctx, set.WithoutRedundantContextLimits()); err != nil {
		return fmt.Errorf("persist llm profiles: %w", err)
	}
	// 注册表是从配置集派生出来的缓存，构造失败沿用旧的即可，不算保存失败。
	if registry, _, err := llm.NewProviderRegistryFromProfiles(set); err == nil {
		document := registry.Document()
		if err := s.store.SaveLLMProviderRegistry(s.ctx, document); err != nil {
			return fmt.Errorf("persist llm provider registry: %w", err)
		}
		s.mu.Lock()
		s.registry = document
		s.mu.Unlock()
	}
	return nil
}
