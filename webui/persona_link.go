// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// 人设库绑定的控制台侧：保存群配置时按绑定取库里的最新内容，库保存或删除时把
// 变化写到绑定它的机器人和群。为什么是「保存时同步写入」而不是运行时去库里取，
// 见 model/assistant/persona_link.go。

// findLibraryPersona 在人设库里找一套。库不可用时当作找不到。
func (h *BotHandler) findLibraryPersona(ctx context.Context, id string) (assistant.Persona, bool) {
	id = strings.TrimSpace(id)
	if h == nil || h.sqlite == nil || id == "" {
		return assistant.Persona{}, false
	}
	set, _, err := h.sqlite.LoadBotPersonas(ctx)
	if err != nil {
		return assistant.Persona{}, false
	}
	return set.WithDefaults().Find(id)
}

// resolveGroupPersonaLink 在保存群配置前兑现绑定：绑着的就用库里最新的五项覆盖，
// 库里已经没有这一套了就解除绑定、保留现有文字。前端填进来的内容只是预览，以库为准。
func (h *BotHandler) resolveGroupPersonaLink(ctx context.Context, cfg assistant.GroupConfig) assistant.GroupConfig {
	cfg.PersonaID = strings.TrimSpace(cfg.PersonaID)
	if cfg.PersonaID == "" {
		return cfg
	}
	persona, ok := h.findLibraryPersona(ctx, cfg.PersonaID)
	if !ok {
		cfg.PersonaID = ""
		return cfg
	}
	return cfg.WithLibraryPersona(persona)
}

// keepGroupPersonaLink 给不认识 persona_id 的调用方（群管理自助接口、旧页面）用：
// 请求里没带绑定，但人设五项和已绑定的那一套完全一样，说明没人动过人设，绑定照留。
// 动过任何一项就是改成本群自定义，绑定解除。
func (h *BotHandler) keepGroupPersonaLink(ctx context.Context, cfg, current assistant.GroupConfig) assistant.GroupConfig {
	if strings.TrimSpace(cfg.PersonaID) != "" || strings.TrimSpace(current.PersonaID) == "" {
		return cfg
	}
	if persona, ok := h.findLibraryPersona(ctx, current.PersonaID); ok && cfg.MatchesLibraryPersona(persona) {
		cfg.PersonaID = current.PersonaID
	}
	return cfg
}

// syncGroupsLinkedToPersona 把库里刚保存的这一套写进所有绑定它的群，返回改了几个。
func (h *BotHandler) syncGroupsLinkedToPersona(persona assistant.Persona) (int, error) {
	if h == nil || h.groupConfigs == nil {
		return 0, nil
	}
	updated := 0
	for _, cfg := range h.groupConfigs.Groups().GroupsLinkedToPersona(persona.ID) {
		if cfg.MatchesLibraryPersona(persona) {
			continue
		}
		if _, err := h.groupConfigs.SaveGroupConfig(cfg.WithLibraryPersona(persona), h.botConfigForProfile(cfg.BotProfileID)); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// unlinkGroupsFromPersona 在库里删掉一套之后调用：绑定它的群保留现有文字，改成
// 本群自定义。不能清空——那会让这些群悄悄换回机器人的人设。
func (h *BotHandler) unlinkGroupsFromPersona(personaID string) (int, error) {
	if h == nil || h.groupConfigs == nil {
		return 0, nil
	}
	updated := 0
	for _, cfg := range h.groupConfigs.Groups().GroupsLinkedToPersona(personaID) {
		cfg.PersonaID = ""
		if _, err := h.groupConfigs.SaveGroupConfig(cfg, h.botConfigForProfile(cfg.BotProfileID)); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// syncBotsLinkedToPersona 把库里刚保存的这一套写进所有绑定它的机器人，并让运行时
// 立刻用上。返回改了几台。
func (h *BotHandler) syncBotsLinkedToPersona(persona assistant.Persona) (int, error) {
	if h == nil || h.profiles == nil {
		return 0, nil
	}
	set, changed := h.profiles.Profiles().WithDefaults().SyncLibraryPersona(persona)
	return len(changed), h.commitProfileChanges(set, changed)
}

// unlinkBotsFromPersona 在库里删掉一套之后调用：绑定它的机器人保留现有人设，解除绑定。
func (h *BotHandler) unlinkBotsFromPersona(personaID string) (int, error) {
	if h == nil || h.profiles == nil {
		return 0, nil
	}
	set, changed := h.profiles.Profiles().WithDefaults().UnlinkLibraryPersona(strings.TrimSpace(personaID))
	return len(changed), h.commitProfileChanges(set, changed)
}

// commitProfileChanges 逐台落库改过的机器人配置，再整体交给运行时。
func (h *BotHandler) commitProfileChanges(set assistant.ProfileSet, changedIDs []string) error {
	if len(changedIDs) == 0 {
		return nil
	}
	for _, id := range changedIDs {
		cfg, ok := set.ConfigForProfile(id)
		if !ok {
			continue
		}
		if err := h.profiles.SaveProfileConfig(cfg); err != nil {
			return err
		}
	}
	if err := h.applyProfileSet(set); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		return err
	}
	return nil
}
