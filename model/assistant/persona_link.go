// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"strings"
)

// 人设库是机器人和群共用的人设来源：机器人或群都可以绑定库里的一套，库里改了
// 自动同步到所有绑定它的地方。
//
// 这和 persona_library.go 顶部「套用来源，不是活绑定」那条并不冲突：那条防的是
// 「界面上写着 A、实际跑的是 B」。这里不在运行时去库里取，而是库保存的那一刻
// 把新内容写进每个绑定它的配置——配置里存的始终就是正在生效的文字，界面看到的
// 和运行的是同一份。绑定只决定「库改了要不要跟着写过来」。
//
// 机器人同步全部人设项（含品格层和人设档位），写法和控制台「套用人设」一致；
// 群只能覆盖正文、表达风格、动作描写、自称、句尾语气词五项。

// WithLibraryPersona 把人设库里的一套写进机器人配置，并记下绑定。和前端
// applyPersonaSettings(current, persona, true) 同一套规则：库里的正文为空时保留
// 现有正文，品格层和分时段语气没填时保留现有的，其余没填的按默认。
func (cfg BotConfig) WithLibraryPersona(persona Persona) BotConfig {
	cfg.PersonaID = persona.ID
	if strings.TrimSpace(persona.SystemPrompt) != "" {
		cfg.SystemPrompt = persona.SystemPrompt
	}
	if persona.Soul != nil {
		cfg.Soul = persona.Soul.Clone()
	}
	cfg.PersonaMode = persona.PersonaMode
	if cfg.PersonaMode == "" {
		cfg.PersonaMode = PersonaModeFill
	}
	cfg.ActionDescriptionEnabled = boolPointer(boolValue(persona.ActionDescriptionEnabled, false))
	if persona.DaypartToneEnabled != nil {
		cfg.DaypartToneEnabled = copyBoolPointer(persona.DaypartToneEnabled)
	}
	cfg.SelfReference = persona.SelfReference
	cfg.SentenceEnders = persona.SentenceEnders
	return cfg
}

// samePersonaFields 判断两份机器人配置的人设项是否完全一样，用来跳过没变化的同步。
func (cfg BotConfig) samePersonaFields(other BotConfig) bool {
	return cfg.PersonaID == other.PersonaID &&
		cfg.SystemPrompt == other.SystemPrompt &&
		reflect.DeepEqual(cfg.Soul, other.Soul) &&
		cfg.PersonaMode == other.PersonaMode &&
		boolValue(cfg.ActionDescriptionEnabled, false) == boolValue(other.ActionDescriptionEnabled, false) &&
		reflect.DeepEqual(cfg.DaypartToneEnabled, other.DaypartToneEnabled) &&
		cfg.SelfReference == other.SelfReference &&
		cfg.SentenceEnders == other.SentenceEnders
}

// SyncLibraryPersona 把库里这一套写进所有绑定它的机器人，返回新的配置集和改了
// 哪几台。
func (s ProfileSet) SyncLibraryPersona(persona Persona) (ProfileSet, []string) {
	var changed []string
	next := s
	next.Profiles = append([]BotConfig(nil), s.Profiles...)
	for index, cfg := range next.Profiles {
		if persona.ID == "" || cfg.PersonaID != persona.ID {
			continue
		}
		updated := cfg.WithLibraryPersona(persona)
		if updated.samePersonaFields(cfg) {
			continue
		}
		next.Profiles[index] = updated
		changed = append(changed, cfg.ID)
	}
	return next, changed
}

// UnlinkLibraryPersona 在库里删掉一套后调用：绑定它的机器人保留现有人设，解除绑定。
func (s ProfileSet) UnlinkLibraryPersona(personaID string) (ProfileSet, []string) {
	var changed []string
	next := s
	next.Profiles = append([]BotConfig(nil), s.Profiles...)
	for index, cfg := range next.Profiles {
		if personaID == "" || cfg.PersonaID != personaID {
			continue
		}
		next.Profiles[index].PersonaID = ""
		changed = append(changed, cfg.ID)
	}
	return next, changed
}

// WithLibraryPersona 把人设库里的一套写进群配置，并记下绑定。
func (cfg GroupConfig) WithLibraryPersona(persona Persona) GroupConfig {
	cfg.PersonaID = persona.ID
	cfg.SystemPrompt = persona.SystemPrompt
	cfg.ReplyStyle = persona.ReplyStyle
	// 没填按关闭，和机器人级套用人设时一样：绑定了就不再跟随机器人的开关。
	cfg.ActionDescriptionEnabled = boolPointer(boolValue(persona.ActionDescriptionEnabled, false))
	cfg.SelfReference = persona.SelfReference
	cfg.SentenceEnders = persona.SentenceEnders
	return cfg
}

// MatchesLibraryPersona 判断群配置里的人设五项是不是正好等于这一套。
func (cfg GroupConfig) MatchesLibraryPersona(persona Persona) bool {
	return strings.TrimSpace(cfg.SystemPrompt) == strings.TrimSpace(persona.SystemPrompt) &&
		cfg.ReplyStyle == persona.ReplyStyle &&
		boolValue(cfg.ActionDescriptionEnabled, false) == boolValue(persona.ActionDescriptionEnabled, false) &&
		strings.TrimSpace(cfg.SelfReference) == strings.TrimSpace(persona.SelfReference) &&
		strings.TrimSpace(cfg.SentenceEnders) == strings.TrimSpace(persona.SentenceEnders)
}

// GroupsLinkedToPersona 返回绑定在这一套人设上的群配置。
func (s GroupConfigSet) GroupsLinkedToPersona(personaID string) []GroupConfig {
	personaID = strings.TrimSpace(personaID)
	if personaID == "" {
		return nil
	}
	var linked []GroupConfig
	for _, cfg := range s.Groups {
		if cfg.PersonaID == personaID {
			linked = append(linked, cfg)
		}
	}
	return linked
}
