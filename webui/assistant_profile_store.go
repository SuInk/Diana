// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

type BotProfileStore interface {
	Profiles() assistant.ProfileSet
	SaveProfiles(assistant.ProfileSet) error
	// SaveProfileConfig 按机器人 ID 覆盖这台机器人的配置。
	SaveProfileConfig(assistant.BotConfig) error
}

type MemoryBotProfileStore struct {
	botProfileChangeNotifier
	mu   sync.RWMutex
	data assistant.ProfileSet
}

// NewMemoryBotProfileStore 创建只有一台机器人的内存版配置集存储。
func NewMemoryBotProfileStore(cfg assistant.BotConfig) *MemoryBotProfileStore {
	return &MemoryBotProfileStore{data: assistant.NewProfileSet(cfg)}
}

// NewMemoryBotProfileStoreFromSet 用现成的配置集创建内存版存储。
func NewMemoryBotProfileStoreFromSet(set assistant.ProfileSet) *MemoryBotProfileStore {
	return &MemoryBotProfileStore{data: set.WithDefaults()}
}

// Profiles 返回内存存储中的机器人配置集。
func (s *MemoryBotProfileStore) Profiles() assistant.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 更新内存中的机器人配置集。
func (s *MemoryBotProfileStore) SaveProfiles(set assistant.ProfileSet) (err error) {
	// 先注册后执行：defer 后进先出，这一条最后跑，那时写锁已经放开。
	defer func() {
		if err == nil {
			s.notifyChanged()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = set.WithDefaults()
	return nil
}

// SaveProfileConfig 按机器人 ID 覆盖内存里这台机器人的配置。
func (s *MemoryBotProfileStore) SaveProfileConfig(cfg assistant.BotConfig) (err error) {
	defer func() {
		if err == nil {
			s.notifyChanged()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := upsertProfileConfig(s.data, cfg)
	if err != nil {
		return err
	}
	s.data = next
	return nil
}

type PersistentBotProfileStore struct {
	botProfileChangeNotifier
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
	}
	return &PersistentBotProfileStore{
		data:  data.WithDefaults(),
		store: store,
		ctx:   ctx,
	}, nil
}

// Profiles 返回持久化存储中的机器人配置集。
func (s *PersistentBotProfileStore) Profiles() assistant.ProfileSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.WithDefaults()
}

// SaveProfiles 保存机器人配置集。
// 落库失败必须往上抛:以前这里把错误丢了,磁盘写不进去时接口照样回 200,
// 前端提示「保存成功」,重启后配置又变回旧值,查起来完全没有线索。
func (s *PersistentBotProfileStore) SaveProfiles(set assistant.ProfileSet) (err error) {
	defer func() {
		if err == nil {
			s.notifyChanged()
		}
	}()
	set = set.WithDefaults()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persist(set); err != nil {
		return err
	}
	s.data = set
	return nil
}

// SaveProfileConfig 按机器人 ID 覆盖这台机器人的配置并落库。
func (s *PersistentBotProfileStore) SaveProfileConfig(cfg assistant.BotConfig) (err error) {
	defer func() {
		if err == nil {
			s.notifyChanged()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	set, err := upsertProfileConfig(s.data, cfg)
	if err != nil {
		return err
	}
	if err := s.persist(set); err != nil {
		return err
	}
	s.data = set
	return nil
}

// persist 把配置集写进存储。
func (s *PersistentBotProfileStore) persist(set assistant.ProfileSet) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.SaveBotProfiles(s.ctx, set); err != nil {
		return fmt.Errorf("persist diana profiles: %w", err)
	}
	return nil
}

// upsertProfileConfig 按 ID 覆盖一台机器人的配置。老调用方不带 ID 时，只有一台
// 机器人才能确定写给它；多台时报错，不写到别的机器人名下。
func upsertProfileConfig(set assistant.ProfileSet, cfg assistant.BotConfig) (assistant.ProfileSet, error) {
	set = set.WithDefaults()
	if strings.TrimSpace(cfg.ID) == "" {
		if len(set.Profiles) != 1 {
			return set, fmt.Errorf("保存机器人配置时缺少机器人 ID")
		}
		cfg.ID = set.Profiles[0].ID
	}
	for i := range set.Profiles {
		if set.Profiles[i].ID != cfg.ID {
			continue
		}
		current := set.Profiles[i]
		if cfg.Name == "" {
			cfg.Name = current.Name
		}
		if cfg.Platform == "" {
			cfg.Platform = current.Platform
		}
		if cfg.AvatarURL == "" {
			cfg.AvatarURL = current.AvatarURL
		}
		set.Profiles[i] = cfg.WithDefaults()
		return set.WithDefaults(), nil
	}
	return set, fmt.Errorf("机器人 %s 不存在", cfg.ID)
}
