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

// NewMemoryLLMProfileStore 创建内存版 LLM 配置集存储。
func NewMemoryLLMProfileStore(cfg llm.ProviderConfig) *MemoryLLMProfileStore {
	return &MemoryLLMProfileStore{data: llm.NewProfileSet(cfg)}
}

// Current 返回内存存储中的当前 LLM 配置。
func (s *MemoryLLMProfileStore) Current() llm.ProviderConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if profile, ok := s.data.Current(); ok {
		return profile.Config.WithDefaults()
	}
	return llm.ProviderConfig{}
}

// Profiles 返回内存存储中的 LLM 配置集。
func (s *MemoryLLMProfileStore) Profiles() llm.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 更新内存中的 LLM 配置集。
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

// NewPersistentLLMProfileStore 创建 SQLite 持久化版 LLM 配置集存储。
func NewPersistentLLMProfileStore(ctx context.Context, store *storage.SQLiteStore, fallback llm.ProviderConfig) (*PersistentLLMProfileStore, error) {
	data := llm.NewProfileSet(fallback)
	if saved, ok, err := store.LoadLLMProfiles(ctx); err != nil {
		return nil, err
	} else if ok && len(saved.Profiles) > 0 {
		data = saved.WithDefaults()
	} else if savedCfg, ok, err := store.LoadLLMConfig(ctx); err != nil {
		return nil, err
	} else if ok {
		// 兼容旧版本只有单个 llm_config 的数据库，首次启动时自动升级为配置集。
		data = llm.NewProfileSet(savedCfg)
	}
	migrated, err := clearLegacyContextWindowFallback(ctx, store, data)
	if err != nil {
		return nil, err
	}
	data = migrated
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

// Current 返回持久化存储中的当前 LLM 配置。
func (s *PersistentLLMProfileStore) Current() llm.ProviderConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if profile, ok := s.data.Current(); ok {
		return profile.Config.WithDefaults()
	}
	return llm.ProviderConfig{}
}

// Profiles 返回持久化存储中的 LLM 配置集。
func (s *PersistentLLMProfileStore) Profiles() llm.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 保存 LLM 配置集并同步当前 flat 配置。
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
	// 否则当前兜底值会被写死进数据库，日后改兜底或改推断表都追不回来——旧版本
	// 的 16K 兜底就是这样把老部署永久钉在 16K 上下文的。
	stored := set.WithoutRedundantContextLimits()
	// 同时写 profile set 和旧 flat config，旧代码/测试读取 llm_config 时仍能拿到当前配置。
	if err := s.store.SaveLLMProfiles(s.ctx, stored); err != nil {
		return fmt.Errorf("persist llm profiles: %w", err)
	}
	if profile, ok := set.Current(); ok {
		if err := s.store.SaveLLMConfig(s.ctx, profile.Config.WithoutRedundantContextLimits()); err != nil {
			return fmt.Errorf("persist active llm profile: %w", err)
		}
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

// clearLegacyContextWindowFallback 一次性把旧版兜底常量 16384 当作「未设置」清掉。
//
// 旧版本的 WithDefaults 会把当时的兜底窗口写进配置并落库，升级后这个显式
// 值优先级高于新兜底和模型名推断，窗口就永远停在 16K。清理只跑一次并记录标记，
// 用户之后自己填的 16384 不会再被动过。
func clearLegacyContextWindowFallback(ctx context.Context, store *storage.SQLiteStore, set llm.ProfileSet) (llm.ProfileSet, error) {
	if store == nil {
		return set.WithDefaults(), nil
	}
	done, err := store.LoadLLMContextWindowMigration(ctx)
	if err != nil {
		return llm.ProfileSet{}, err
	}
	if done {
		return set.WithDefaults(), nil
	}
	cleared, changed := set.ClearLegacyContextFallback()
	if changed {
		if err := store.SaveLLMProfiles(ctx, cleared); err != nil {
			return llm.ProfileSet{}, err
		}
		if profile, ok := cleared.Current(); ok {
			if err := store.SaveLLMConfig(ctx, profile.Config.WithoutRedundantContextLimits()); err != nil {
				return llm.ProfileSet{}, err
			}
		}
	}
	if err := store.SaveLLMContextWindowMigration(ctx, true); err != nil {
		return llm.ProfileSet{}, err
	}
	return cleared.WithDefaults(), nil
}
