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

	"github.com/google/uuid"
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
	// legacyAliases 把种子机器人修复前用过的旧档案 ID 对到它现在的固定 ID。
	legacyAliases map[string]string
}

// NewPersistentBotProfileStore 创建 SQLite 持久化版 OneBot v11 机器人配置集存储。
//
// WebUI 保存过的配置集直接用；没保存过就从 config.yaml 播种，但种子机器人的档案 ID
// 取库里钉住的那一个，重启不变。
func NewPersistentBotProfileStore(ctx context.Context, store *storage.SQLiteStore, fallback assistant.BotConfig) (*PersistentBotProfileStore, error) {
	saved, ok, err := store.LoadBotProfiles(ctx)
	if err != nil {
		return nil, err
	}
	var data assistant.ProfileSet
	var seed storage.SeedProfile
	if ok && len(saved.Profiles) > 0 {
		// 保存过就不再需要种子 ID，只读出以前钉过的旧号映射（如果有）。
		if seed, _, err = store.LoadBotSeedProfile(ctx); err != nil {
			return nil, err
		}
		data = saved
	} else {
		if seed, err = stableBotSeedProfile(ctx, store, ok); err != nil {
			return nil, err
		}
		data = assistant.NewProfileSet(fallback)
		data.Profiles[0].ID = seed.ID
	}
	return &PersistentBotProfileStore{
		data:          data.WithDefaults(),
		store:         store,
		ctx:           ctx,
		legacyAliases: legacyProfileAliases(seed),
	}, nil
}

// stableBotSeedProfile 取出（第一次时生成并落库）种子机器人的固定档案 ID。
//
// 只钉 ID、不把整份种子配置落库：落了库，config.yaml 里的机器人参数就再也改不动了，
// 这对只靠 config.yaml 管理的部署是行为变化。ID 用随机值而不是由固定输入算出来：
// 几个实例共用编码任务记录目录时靠档案 ID 区分彼此的任务，算出来的 ID 会让所有
// 实例撞成同一个。
//
// 第一次钉 ID 时，如果库里从来没存过配置集，说明这一直是只靠播种跑的部署，历史
// 消息里出现过的档案 ID 全是这台种子机器人每次重启换的新号。最近用过的那个接着用，
// 上次重启以来攒下的记忆和群配置不至于再丢一轮；其余记成旧号，供编码任务认领。
// 存过配置集的库不收集：那里的历史 ID 可能属于已经删掉的机器人，认不得。
func stableBotSeedProfile(ctx context.Context, store *storage.SQLiteStore, profilesSaved bool) (storage.SeedProfile, error) {
	seed, ok, err := store.LoadBotSeedProfile(ctx)
	if err != nil {
		return storage.SeedProfile{}, err
	}
	if ok && strings.TrimSpace(seed.ID) != "" {
		return seed, nil
	}
	seed = storage.SeedProfile{}
	if !profilesSaved {
		history, err := store.RecentBotProfileIDs(ctx)
		if err != nil {
			return storage.SeedProfile{}, err
		}
		if len(history) > 0 {
			seed.ID, seed.LegacyIDs = history[0], history[1:]
		}
	}
	if seed.ID == "" {
		seed.ID = uuid.NewString()
	}
	if err := store.SaveBotSeedProfile(ctx, seed); err != nil {
		return storage.SeedProfile{}, fmt.Errorf("persist diana seed profile id: %w", err)
	}
	return seed, nil
}

// legacyProfileAliases 把旧号映射到种子机器人现在的 ID。
func legacyProfileAliases(seed storage.SeedProfile) map[string]string {
	target := strings.TrimSpace(seed.ID)
	if target == "" || len(seed.LegacyIDs) == 0 {
		return nil
	}
	aliases := make(map[string]string, len(seed.LegacyIDs))
	for _, id := range seed.LegacyIDs {
		if id = strings.TrimSpace(id); id != "" && id != target {
			aliases[id] = target
		}
	}
	return aliases
}

// LegacyProfileAliases 返回种子机器人修复前用过的旧档案 ID → 现在的 ID。
func (s *PersistentBotProfileStore) LegacyProfileAliases() map[string]string {
	out := make(map[string]string, len(s.legacyAliases))
	for legacy, current := range s.legacyAliases {
		out[legacy] = current
	}
	return out
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
