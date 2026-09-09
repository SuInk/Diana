package webui

import (
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

func updateStoredModelRole(set assistant.ProfileSet, expected assistant.BotConfig, role string, next assistant.ModelRole) (assistant.ProfileSet, assistant.BotConfig, error) {
	set = set.WithDefaults()
	for i, cfg := range set.Profiles {
		if cfg.ID != expected.ID {
			continue
		}
		if cfg.OwnerID != expected.OwnerID || (cfg.OwnerLLMConfigEnabled != nil && !*cfg.OwnerLLMConfigEnabled) {
			return set, cfg, fmt.Errorf("主人权限或配置开关已变化，请重新请求")
		}
		roles := make(map[string]assistant.ModelRole, len(cfg.ModelRoles)+1)
		for key, value := range cfg.ModelRoles {
			roles[key] = value
		}
		roles[role] = next
		cfg.ModelRoles = roles
		cfg = cfg.WithDefaults()
		set.Profiles = append([]assistant.BotConfig(nil), set.Profiles...)
		set.Profiles[i] = cfg
		return set, cfg, nil
	}
	return set, assistant.BotConfig{}, fmt.Errorf("机器人 %q 已不存在", expected.ID)
}

func (s *MemoryBotProfileStore) SaveModelRole(cfg assistant.BotConfig, role string, next assistant.ModelRole) (assistant.BotConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, saved, err := updateStoredModelRole(s.data, cfg, role, next)
	if err != nil {
		return assistant.BotConfig{}, err
	}
	s.data = set
	return saved, nil
}

func (s *PersistentBotProfileStore) SaveModelRole(cfg assistant.BotConfig, role string, next assistant.ModelRole) (assistant.BotConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, saved, err := updateStoredModelRole(s.data, cfg, role, next)
	if err != nil {
		return assistant.BotConfig{}, err
	}
	if err := s.persist(set); err != nil {
		return assistant.BotConfig{}, err
	}
	s.data = set
	return saved, nil
}
