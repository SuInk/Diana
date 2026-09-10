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
	DeleteGroupConfig(botProfileID, groupID string) (bool, error)
}

// BotProfileSource 只要求「能报出全部机器人配置」，BotProfileStore 天然满足。
// 群配置存储拿它把每个群解析回自己那台机器人，不去碰全局变量。
type BotProfileSource interface {
	Profiles() assistant.ProfileSet
}

// botGroupConfigProfileAware 是可选能力：注入了机器人配置来源的群配置存储，
// 归一化时会按各群的 bot_profile_id 分别取 base。
type botGroupConfigProfileAware interface {
	SetProfileSource(BotProfileSource)
}

// botConfigResolver 把机器人配置来源包成解析器；没有来源时返回 nil，
// 调用方退回传进来的 base，与改造前行为一致。
func botConfigResolver(source BotProfileSource) assistant.BotConfigResolver {
	if source == nil {
		return nil
	}
	return func(profileID string) (assistant.BotConfig, bool) {
		return source.Profiles().ConfigForProfile(profileID)
	}
}

type MemoryBotGroupConfigStore struct {
	mu       sync.RWMutex
	data     assistant.GroupConfigSet
	profiles BotProfileSource
}

func NewMemoryBotGroupConfigStore() *MemoryBotGroupConfigStore {
	return &MemoryBotGroupConfigStore{data: assistant.GroupConfigSet{}}
}

// SetProfileSource 注入机器人配置来源，让群配置跟随自己那台机器人。
func (s *MemoryBotGroupConfigStore) SetProfileSource(source BotProfileSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles = source
}

func (s *MemoryBotGroupConfigStore) resolver() assistant.BotConfigResolver {
	s.mu.RLock()
	source := s.profiles
	s.mu.RUnlock()
	return botConfigResolver(source)
}

func withoutGroupConfig(set assistant.GroupConfigSet, profileID, groupID string) (assistant.GroupConfigSet, bool) {
	next := set
	next.Groups = make([]assistant.GroupConfig, 0, len(set.Groups))
	for _, cfg := range set.Groups {
		if cfg.BotProfileID != profileID || cfg.GroupID != groupID {
			next.Groups = append(next.Groups, cfg)
		}
	}
	return next, len(next.Groups) != len(set.Groups)
}

func (s *MemoryBotGroupConfigStore) DeleteGroupConfig(profileID, groupID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, found := withoutGroupConfig(s.data, profileID, groupID)
	s.data = next
	return found, nil
}

func (s *PersistentBotGroupConfigStore) DeleteGroupConfig(profileID, groupID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, found := withoutGroupConfig(s.data, profileID, groupID)
	if !found {
		return false, nil
	}
	if s.store != nil {
		if err := s.store.SaveBotGroupConfigs(s.ctx, next); err != nil {
			return false, err
		}
	}
	s.data = next
	return true, nil
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
	resolve := s.resolver()
	cfg = cfg.WithDefaultsResolved(cfg.GroupID, base, resolve)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = s.data.UpsertResolved(cfg, base, resolve)
	saved, _ := s.data.ConfigForGroup(cfg.BotProfileID, cfg.GroupID)
	return saved, nil
}

type PersistentBotGroupConfigStore struct {
	mu       sync.RWMutex
	data     assistant.GroupConfigSet
	store    *storage.SQLiteStore
	ctx      context.Context
	profiles BotProfileSource
}

// SetProfileSource 注入机器人配置来源，让群配置跟随自己那台机器人。
func (s *PersistentBotGroupConfigStore) SetProfileSource(source BotProfileSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles = source
}

func (s *PersistentBotGroupConfigStore) resolver() assistant.BotConfigResolver {
	s.mu.RLock()
	source := s.profiles
	s.mu.RUnlock()
	return botConfigResolver(source)
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
	resolve := s.resolver()
	cfg = cfg.WithDefaultsResolved(cfg.GroupID, base, resolve)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 保存一个群会顺手归一化整份数据，所以这里必须按各群自己的机器人取 base：
	// 否则别的机器人的群会被当前这台的人设和默认值覆写。
	set := s.data.WithDefaultsResolved(base, resolve).UpsertResolved(cfg, base, resolve)
	saved, _ := set.ConfigForGroup(cfg.BotProfileID, cfg.GroupID)
	if s.store != nil {
		if err := s.store.SaveBotGroupConfigs(s.ctx, set); err != nil {
			return saved, err
		}
	}
	s.data = set
	return saved, nil
}
