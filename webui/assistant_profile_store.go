// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

type BotProfileStore interface {
	Current() assistant.BotConfig
	Profiles() assistant.ProfileSet
	SaveProfiles(assistant.ProfileSet)
	SaveCurrentConfig(assistant.BotConfig)
}

type MemoryBotProfileStore struct {
	mu   sync.RWMutex
	data assistant.ProfileSet
}

// NewMemoryBotProfileStore 创建内存版 OneBot v11 机器人配置集存储。
func NewMemoryBotProfileStore(cfg assistant.BotConfig) *MemoryBotProfileStore {
	return &MemoryBotProfileStore{data: assistant.NewProfileSet(cfg)}
}

// Current 返回内存存储中的当前机器人配置。
func (s *MemoryBotProfileStore) Current() assistant.BotConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if profile, ok := s.data.Current(); ok {
		return profile.WithDefaults()
	}
	return assistant.BotConfig{}
}

// Profiles 返回内存存储中的机器人配置集。
func (s *MemoryBotProfileStore) Profiles() assistant.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 更新内存中的机器人配置集。
func (s *MemoryBotProfileStore) SaveProfiles(set assistant.ProfileSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = set.WithDefaults()
}

// SaveCurrentConfig 把运行时当前配置写回当前激活的机器人档案。
func (s *MemoryBotProfileStore) SaveCurrentConfig(cfg assistant.BotConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = upsertCurrentBotProfileSet(s.data, cfg)
}

type PersistentBotProfileStore struct {
	mu    sync.RWMutex
	data  assistant.ProfileSet
	store *storage.SQLiteStore
	ctx   context.Context
}

// NewPersistentBotProfileStore 创建 SQLite 持久化版 OneBot v11 机器人配置集存储。
func NewPersistentBotProfileStore(ctx context.Context, store *storage.SQLiteStore, fallback assistant.BotConfig) (*PersistentBotProfileStore, error) {
	data := assistant.NewProfileSet(fallback)
	if saved, ok, err := store.LoadBotProfiles(ctx); err != nil {
		return nil, err
	} else if ok && len(saved.Profiles) > 0 {
		data = saved.WithDefaults()
	} else if savedCfg, ok, err := store.LoadBotProfileConfig(ctx); err != nil {
		return nil, err
	} else if ok {
		// 兼容旧版只有单个 qqbot_config 的数据库，首次启动时自动升级为配置集。
		data = assistant.NewProfileSet(savedCfg)
	}
	return &PersistentBotProfileStore{
		data:  data.WithDefaults(),
		store: store,
		ctx:   ctx,
	}, nil
}

// Current 返回持久化存储中的当前机器人配置。
func (s *PersistentBotProfileStore) Current() assistant.BotConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if profile, ok := s.data.Current(); ok {
		return profile.WithDefaults()
	}
	return assistant.BotConfig{}
}

// Profiles 返回持久化存储中的机器人配置集。
func (s *PersistentBotProfileStore) Profiles() assistant.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 保存机器人配置集并同步旧版 flat 配置。
func (s *PersistentBotProfileStore) SaveProfiles(set assistant.ProfileSet) {
	set = set.WithDefaults()
	s.mu.Lock()
	s.data = set
	s.mu.Unlock()
	if s.store != nil {
		_ = s.store.SaveBotProfiles(s.ctx, set)
		if profile, ok := set.Current(); ok {
			_ = s.store.SaveBotProfileConfig(s.ctx, profile)
		}
	}
}

// SaveCurrentConfig 把运行时当前配置回写到激活中的机器人配置档。
func (s *PersistentBotProfileStore) SaveCurrentConfig(cfg assistant.BotConfig) {
	s.mu.Lock()
	s.data = upsertCurrentBotProfileSet(s.data, cfg)
	set := s.data
	s.mu.Unlock()
	if s.store != nil {
		_ = s.store.SaveBotProfiles(s.ctx, set)
		if profile, ok := set.Current(); ok {
			_ = s.store.SaveBotProfileConfig(s.ctx, profile)
		}
	}
}

// upsertCurrentBotProfileSet 用最新运行态覆盖当前激活的机器人配置档。
func upsertCurrentBotProfileSet(set assistant.ProfileSet, cfg assistant.BotConfig) assistant.ProfileSet {
	set = set.WithDefaults()
	current, ok := set.Current()
	if cfg.ID == "" && ok {
		cfg.ID = current.ID
	}
	if cfg.Name == "" && ok {
		cfg.Name = current.Name
	}
	if cfg.Platform == "" && ok {
		cfg.Platform = current.Platform
	}
	if cfg.AvatarURL == "" && ok {
		cfg.AvatarURL = current.AvatarURL
	}
	cfg = cfg.WithDefaults()
	for i := range set.Profiles {
		if set.Profiles[i].ID != cfg.ID {
			continue
		}
		set.Profiles[i] = cfg
		set.ActiveID = cfg.ID
		return set.WithDefaults()
	}
	set.Profiles = append(set.Profiles, cfg)
	set.ActiveID = cfg.ID
	return set.WithDefaults()
}
