package webui

import (
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

func updatedParticipationSet(set assistant.ProfileSet, expected assistant.BotConfig, prefs assistant.ParticipationPreferences) (assistant.ProfileSet, assistant.BotConfig, error) {
	set = set.WithDefaults()
	for i, cfg := range set.Profiles {
		if cfg.ID != expected.ID {
			continue
		}
		if cfg.OwnerID != expected.OwnerID {
			return set, cfg, fmt.Errorf("主人配置已变化，请重新请求")
		}
		cfg.Participation = &prefs
		cfg.ResponseMode = assistant.ResponseModeCustom
		cfg.NaturalInterjectionEnabled = new(bool)
		cfg = cfg.WithDefaults()
		set.Profiles = append([]assistant.BotConfig(nil), set.Profiles...)
		set.Profiles[i] = cfg
		return set, cfg, nil
	}
	return set, assistant.BotConfig{}, fmt.Errorf("机器人已不存在")
}
func (s *MemoryBotProfileStore) SaveParticipation(cfg assistant.BotConfig, prefs assistant.ParticipationPreferences) (assistant.BotConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, saved, err := updatedParticipationSet(s.data, cfg, prefs)
	if err != nil {
		return saved, err
	}
	s.data = set
	return saved, nil
}
func (s *PersistentBotProfileStore) SaveParticipation(cfg assistant.BotConfig, prefs assistant.ParticipationPreferences) (assistant.BotConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, saved, err := updatedParticipationSet(s.data, cfg, prefs)
	if err != nil {
		return saved, err
	}
	if err := s.persist(set); err != nil {
		return assistant.BotConfig{}, err
	}
	s.data = set
	return saved, nil
}
func (p *RuntimePersistor) SaveParticipation(cfg assistant.BotConfig, prefs assistant.ParticipationPreferences) (assistant.BotConfig, error) {
	if p == nil || p.store == nil {
		return assistant.BotConfig{}, fmt.Errorf("未接入机器人配置存储")
	}
	store, ok := p.store.(assistant.BotParticipationConfigSaver)
	if !ok {
		return assistant.BotConfig{}, fmt.Errorf("不支持回复设置保存")
	}
	return store.SaveParticipation(cfg, prefs)
}
