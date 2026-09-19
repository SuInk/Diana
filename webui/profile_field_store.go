// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

// 聊天指令只能改一台机器人的某一个字段（标记的机器人、屏蔽名单、禁用群），接口特意做窄：
// 聊天里下的指令不该有能力覆写整份配置。三者共用下面这套「按机器人改一个字段并落盘」。

func withProfileField(set assistant.ProfileSet, profileID string, mutate func(*assistant.BotConfig)) (assistant.ProfileSet, error) {
	next := set.WithDefaults()
	for i := range next.Profiles {
		if next.Profiles[i].ID == profileID {
			mutate(&next.Profiles[i])
			return next.WithDefaults(), nil
		}
	}
	return assistant.ProfileSet{}, fmt.Errorf("目标机器人不存在")
}

func (s *MemoryBotProfileStore) updateProfileField(profileID string, mutate func(*assistant.BotConfig)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := withProfileField(s.data, profileID, mutate)
	if err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *PersistentBotProfileStore) updateProfileField(profileID string, mutate func(*assistant.BotConfig)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := withProfileField(s.data, profileID, mutate)
	if err != nil {
		return err
	}
	if s.store == nil {
		return fmt.Errorf("配置存储不可用")
	}
	if err = s.store.SaveBotProfiles(s.ctx, next); err != nil {
		return err
	}
	s.data = next
	return nil
}

type profileFieldUpdater interface {
	updateProfileField(profileID string, mutate func(*assistant.BotConfig)) error
}

func (p *RuntimePersistor) updateProfileField(profileID string, mutate func(*assistant.BotConfig)) error {
	if p == nil {
		return fmt.Errorf("配置持久化器不可用")
	}
	store, ok := p.store.(profileFieldUpdater)
	if !ok {
		return fmt.Errorf("配置存储不支持按机器人修改配置")
	}
	return store.updateProfileField(profileID, mutate)
}

func markedBotIDsField(ids []string) func(*assistant.BotConfig) {
	return func(cfg *assistant.BotConfig) { cfg.MarkedBotIDs = append([]string(nil), ids...) }
}

// blockedUsersField 只换掉门禁里的屏蔽名单，门槛和另外两个名单不动：
// 聊天里下的屏蔽指令不该顺手改掉回复时段或等级门槛。
func blockedUsersField(userIDs []string) func(*assistant.BotConfig) {
	return func(cfg *assistant.BotConfig) { cfg.ReplyGate = cfg.ReplyGate.WithBlockedUsers(userIDs) }
}

func disabledGroupsField(groupIDs []string) func(*assistant.BotConfig) {
	return func(cfg *assistant.BotConfig) { cfg.DisabledGroups = append([]string{}, groupIDs...) }
}

func (s *MemoryBotProfileStore) SaveMarkedBotIDs(profileID string, ids []string) error {
	return s.updateProfileField(profileID, markedBotIDsField(ids))
}

func (s *PersistentBotProfileStore) SaveMarkedBotIDs(profileID string, ids []string) error {
	return s.updateProfileField(profileID, markedBotIDsField(ids))
}

func (p *RuntimePersistor) SaveMarkedBotIDs(profileID string, ids []string) error {
	return p.updateProfileField(profileID, markedBotIDsField(ids))
}

func (s *MemoryBotProfileStore) SaveBlockedUsers(profileID string, userIDs []string) error {
	return s.updateProfileField(profileID, blockedUsersField(userIDs))
}

func (s *PersistentBotProfileStore) SaveBlockedUsers(profileID string, userIDs []string) error {
	return s.updateProfileField(profileID, blockedUsersField(userIDs))
}

func (p *RuntimePersistor) SaveBlockedUsers(profileID string, userIDs []string) error {
	return p.updateProfileField(profileID, blockedUsersField(userIDs))
}

func (s *MemoryBotProfileStore) SaveDisabledGroups(profileID string, groupIDs []string) error {
	return s.updateProfileField(profileID, disabledGroupsField(groupIDs))
}

func (s *PersistentBotProfileStore) SaveDisabledGroups(profileID string, groupIDs []string) error {
	return s.updateProfileField(profileID, disabledGroupsField(groupIDs))
}

func (p *RuntimePersistor) SaveDisabledGroups(profileID string, groupIDs []string) error {
	return p.updateProfileField(profileID, disabledGroupsField(groupIDs))
}
