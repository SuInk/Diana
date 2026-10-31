// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"

	"github.com/google/uuid"
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
	// seedID 非空表示启动时库里没有保存过的配置集，这一份是从 config.yaml 播种的，
	// 值就是库里钉住的那个 ID。见 SeedProfileID。
	seedID string
}

// NewPersistentLLMProfileStore 创建 SQLite 持久化版提供商配置集存储。
//
// WebUI 保存过的配置集直接用；没保存过就从 config.yaml 播种，但种子配置档的 ID 取库里
// 钉住的那一个，重启不变——机器人的 model_roles 按这个 ID 引用它。
func NewPersistentLLMProfileStore(ctx context.Context, store *storage.SQLiteStore, fallback llm.ProviderConfig) (*PersistentLLMProfileStore, error) {
	saved, ok, err := store.LoadLLMProfiles(ctx)
	if err != nil {
		return nil, err
	}
	registry, registryOK, err := store.LoadLLMProviderRegistry(ctx)
	if err != nil {
		return nil, err
	}
	var data llm.ProfileSet
	seedID := ""
	if ok && len(saved.Profiles) > 0 {
		data = saved
	} else {
		seed, err := stableLLMSeedProfile(ctx, store, registry, registryOK)
		if err != nil {
			return nil, err
		}
		seedID = seed.ID
		data = llm.NewProfileSet(fallback)
		data.Profiles[0].ID = seedID
	}
	data = data.WithDefaults()
	// 注册表是从配置集派生出来的缓存。播种的配置集每次启动都从 config.yaml 重新读，
	// 注册表也得跟着重建，否则它一直停在第一次启动时的 ID 和凭据上。
	if !registryOK || registry.Version == 0 || seedID != "" {
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
		seedID:   seedID,
	}, nil
}

// stableLLMSeedProfile 取出（第一次时生成并落库）种子提供商配置档的固定 ID。
//
// 和机器人一样只钉 ID、不把整份种子配置落库，config.yaml 里改了 llm 段重启照样生效。
// 第一次钉 ID 时，如果注册表是从某一次启动的种子派生出来的（只有一个提供商），
// 就接着用它的 ID：修复前注册表只在第一次启动时写过一次，它记着的正是那次的 ID。
func stableLLMSeedProfile(ctx context.Context, store *storage.SQLiteStore, registry llm.ProviderRegistryDocument, registryOK bool) (storage.SeedProfile, error) {
	seed, ok, err := store.LoadLLMSeedProfile(ctx)
	if err != nil {
		return storage.SeedProfile{}, err
	}
	if ok && strings.TrimSpace(seed.ID) != "" {
		return seed, nil
	}
	seed = storage.SeedProfile{}
	if registryOK && len(registry.Providers) == 1 {
		seed.ID = strings.TrimSpace(registry.Providers[0].ID)
	}
	if seed.ID == "" {
		seed.ID = uuid.NewString()
	}
	if err := store.SaveLLMSeedProfile(ctx, seed); err != nil {
		return storage.SeedProfile{}, fmt.Errorf("persist llm seed profile id: %w", err)
	}
	return seed, nil
}

// SeedProfileID 返回启动时从 config.yaml 播种的那份配置档的 ID；库里有保存过的
// 配置集时返回空。
func (s *PersistentLLMProfileStore) SeedProfileID() string {
	return s.seedID
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
