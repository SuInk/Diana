// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 群人设可以绑定人设库里的一套，库里改了自动同步到所有绑定它的群。
//
// 这和 persona_library.go 顶部「套用来源，不是活绑定」那条并不冲突：那条防的是
// 「界面上写着 A、实际跑的是 B」。这里不在运行时去库里取，而是库保存的那一刻
// 把新内容写进每个绑定它的群配置——群配置里存的始终就是正在生效的文字，界面
// 看到的和运行的是同一份。绑定只决定「库改了要不要跟着写过来」。
//
// 同步的是群配置能覆盖的五项：正文、表达风格、动作描写、自称、句尾语气词。
// 品格层（Soul）和人设档位（PersonaMode）只在机器人级存在，群里改不了。

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
