package webui

import (
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

// withBlockedUsers 只换掉这台机器人门禁里的屏蔽名单，门槛和另外两个名单不动：
// 聊天里下的屏蔽指令不该顺手改掉回复时段或等级门槛。
func withBlockedUsers(set assistant.ProfileSet, profileID string, userIDs []string) (assistant.ProfileSet, error) {
	next := set.WithDefaults()
	for i := range next.Profiles {
		if next.Profiles[i].ID == profileID {
			next.Profiles[i].ReplyGate = next.Profiles[i].ReplyGate.WithBlockedUsers(userIDs)
			return next.WithDefaults(), nil
		}
	}
	return assistant.ProfileSet{}, fmt.Errorf("目标机器人不存在")
}

func (s *MemoryBotProfileStore) SaveBlockedUsers(profileID string, userIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := withBlockedUsers(s.data, profileID, userIDs)
	if err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *PersistentBotProfileStore) SaveBlockedUsers(profileID string, userIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := withBlockedUsers(s.data, profileID, userIDs)
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

func (p *RuntimePersistor) SaveBlockedUsers(profileID string, userIDs []string) error {
	if p == nil {
		return fmt.Errorf("配置持久化器不可用")
	}
	store, ok := p.store.(interface {
		SaveBlockedUsers(string, []string) error
	})
	if !ok {
		return fmt.Errorf("配置存储不支持按机器人修改屏蔽名单")
	}
	return store.SaveBlockedUsers(profileID, userIDs)
}
