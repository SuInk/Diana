package webui

import (
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

func withMarkedBotIDs(set assistant.ProfileSet, profileID string, ids []string) (assistant.ProfileSet, error) {
	next := set.WithDefaults()
	for i := range next.Profiles {
		if next.Profiles[i].ID == profileID {
			next.Profiles[i].MarkedBotIDs = append([]string(nil), ids...)
			return next.WithDefaults(), nil
		}
	}
	return assistant.ProfileSet{}, fmt.Errorf("目标机器人不存在")
}

func (s *MemoryBotProfileStore) SaveMarkedBotIDs(profileID string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := withMarkedBotIDs(s.data, profileID, ids)
	if err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *PersistentBotProfileStore) SaveMarkedBotIDs(profileID string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := withMarkedBotIDs(s.data, profileID, ids)
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

func (p *RuntimePersistor) SaveMarkedBotIDs(profileID string, ids []string) error {
	if p == nil {
		return fmt.Errorf("配置持久化器不可用")
	}
	store, ok := p.store.(interface{ SaveMarkedBotIDs(string, []string) error })
	if !ok {
		return fmt.Errorf("配置存储不支持按机器人修改标记")
	}
	return store.SaveMarkedBotIDs(profileID, ids)
}
