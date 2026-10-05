package webui

import (
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

func updatedBotSettingsSet(set assistant.ProfileSet, expected assistant.BotConfig, update assistant.BotSettingsUpdate) (assistant.ProfileSet, assistant.BotConfig, error) {
	set = set.WithDefaults()
	for i, cfg := range set.Profiles {
		if cfg.ID != expected.ID {
			continue
		}
		if cfg.OwnerID != expected.OwnerID {
			return set, cfg, fmt.Errorf("主人配置已变化，请重新请求")
		}
		cfg = update.Apply(cfg).WithDefaults()
		set.Profiles = append([]assistant.BotConfig(nil), set.Profiles...)
		set.Profiles[i] = cfg
		return set, cfg, nil
	}
	return set, assistant.BotConfig{}, fmt.Errorf("机器人已不存在")
}
func (s *MemoryBotProfileStore) SaveBotSettings(cfg assistant.BotConfig, update assistant.BotSettingsUpdate) (assistant.BotConfig, error) {
	s.mu.Lock()
	set, saved, err := updatedBotSettingsSet(s.data, cfg, update)
	if err != nil {
		s.mu.Unlock()
		return saved, err
	}
	s.data = set
	s.mu.Unlock()
	s.notifyChanged()
	return saved, nil
}
func (s *PersistentBotProfileStore) SaveBotSettings(cfg assistant.BotConfig, update assistant.BotSettingsUpdate) (assistant.BotConfig, error) {
	s.mu.Lock()
	set, saved, err := updatedBotSettingsSet(s.data, cfg, update)
	if err != nil {
		s.mu.Unlock()
		return saved, err
	}
	if err := s.persist(set); err != nil {
		s.mu.Unlock()
		return assistant.BotConfig{}, err
	}
	s.data = set
	s.mu.Unlock()
	s.notifyChanged()
	return saved, nil
}
func (p *RuntimePersistor) SaveBotSettings(cfg assistant.BotConfig, update assistant.BotSettingsUpdate) (assistant.BotConfig, error) {
	if p == nil || p.store == nil {
		return assistant.BotConfig{}, fmt.Errorf("未接入机器人配置存储")
	}
	store, ok := p.store.(assistant.BotSettingsConfigSaver)
	if !ok {
		return assistant.BotConfig{}, fmt.Errorf("不支持机器人设置保存")
	}
	return store.SaveBotSettings(cfg, update)
}
