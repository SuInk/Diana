// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

type BotGroupConfigStore interface {
	ConfigForGroup(botProfileID, groupID string) (assistant.GroupConfig, bool)
	ConfigForGroupAnyProfile(groupID string) (assistant.GroupConfig, bool)
	Groups() assistant.GroupConfigSet
	SaveGroupConfig(assistant.GroupConfig, assistant.BotConfig) (assistant.GroupConfig, error)
}

type MemoryBotGroupConfigStore struct {
	mu   sync.RWMutex
	data assistant.GroupConfigSet
}

func NewMemoryBotGroupConfigStore() *MemoryBotGroupConfigStore {
	return &MemoryBotGroupConfigStore{data: assistant.GroupConfigSet{}}
}

func (s *MemoryBotGroupConfigStore) ConfigForGroup(botProfileID, groupID string) (assistant.GroupConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ConfigForGroup(botProfileID, groupID)
}

func (s *MemoryBotGroupConfigStore) ConfigForGroupAnyProfile(groupID string) (assistant.GroupConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ConfigForGroupAnyProfile(groupID)
}

func (s *MemoryBotGroupConfigStore) Groups() assistant.GroupConfigSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

func (s *MemoryBotGroupConfigStore) SaveGroupConfig(cfg assistant.GroupConfig, base assistant.BotConfig) (assistant.GroupConfig, error) {
	cfg = cfg.WithDefaults(cfg.GroupID, base)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = s.data.Upsert(cfg, base)
	saved, _ := s.data.ConfigForGroup(cfg.BotProfileID, cfg.GroupID)
	return saved, nil
}

type PersistentBotGroupConfigStore struct {
	mu    sync.RWMutex
	data  assistant.GroupConfigSet
	store *storage.SQLiteStore
	ctx   context.Context
}

func NewPersistentBotGroupConfigStore(ctx context.Context, store *storage.SQLiteStore) (*PersistentBotGroupConfigStore, error) {
	data := assistant.GroupConfigSet{}
	if saved, ok, err := store.LoadBotGroupConfigs(ctx); err != nil {
		return nil, err
	} else if ok {
		data = saved
	}
	return &PersistentBotGroupConfigStore{
		data:  data,
		store: store,
		ctx:   ctx,
	}, nil
}

func (s *PersistentBotGroupConfigStore) ConfigForGroup(botProfileID, groupID string) (assistant.GroupConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ConfigForGroup(botProfileID, groupID)
}

func (s *PersistentBotGroupConfigStore) ConfigForGroupAnyProfile(groupID string) (assistant.GroupConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ConfigForGroupAnyProfile(groupID)
}

func (s *PersistentBotGroupConfigStore) Groups() assistant.GroupConfigSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

func (s *PersistentBotGroupConfigStore) SaveGroupConfig(cfg assistant.GroupConfig, base assistant.BotConfig) (assistant.GroupConfig, error) {
	cfg = cfg.WithDefaults(cfg.GroupID, base)
	s.mu.Lock()
	s.data = s.data.WithDefaults(base).Upsert(cfg, base)
	set := s.data
	saved, _ := set.ConfigForGroup(cfg.BotProfileID, cfg.GroupID)
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.SaveBotGroupConfigs(s.ctx, set); err != nil {
			return saved, err
		}
	}
	return saved, nil
}
