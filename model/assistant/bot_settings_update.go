package assistant

import (
	"fmt"
	"strings"
)

// BotSettingsUpdate contains only fields requested through bot_config. Storage
// applies it to the latest profile so unrelated settings are never overwritten.
type BotSettingsUpdate struct {
	Participation             *ParticipationPreferences
	WelcomeEnabled            *bool
	WelcomeMessage            *string
	WelcomeMode               *WelcomeMode
	WelcomeTemplates          *[]string
	WelcomeLLMCooldownSeconds *int
}

func (u BotSettingsUpdate) UpdatesWelcome() bool {
	return u.WelcomeEnabled != nil || u.WelcomeMessage != nil || u.WelcomeMode != nil || u.WelcomeTemplates != nil || u.WelcomeLLMCooldownSeconds != nil
}

func (u BotSettingsUpdate) Apply(cfg BotConfig) BotConfig {
	if u.Participation != nil {
		cfg.Participation = copyParticipation(u.Participation)
		cfg.ResponseMode = ResponseModeCustom
		cfg.NaturalInterjectionEnabled = boolPointer(false)
	}
	if u.WelcomeEnabled != nil {
		cfg.WelcomeEnabled = *u.WelcomeEnabled
	}
	if u.WelcomeMessage != nil {
		cfg.WelcomeMessage = *u.WelcomeMessage
	}
	if u.WelcomeMode != nil {
		cfg.WelcomeMode = *u.WelcomeMode
	}
	if u.WelcomeTemplates != nil {
		cfg.WelcomeTemplates = append([]string(nil), (*u.WelcomeTemplates)...)
	}
	if u.WelcomeLLMCooldownSeconds != nil {
		cfg.WelcomeLLMCooldownSeconds = *u.WelcomeLLMCooldownSeconds
	}
	return cfg
}

func (u BotSettingsUpdate) applyToGroup(cfg GroupConfig) GroupConfig {
	if u.WelcomeEnabled != nil {
		cfg.WelcomeEnabled = copyBoolPointer(u.WelcomeEnabled)
	}
	if u.WelcomeMessage != nil {
		cfg.WelcomeMessage = *u.WelcomeMessage
	}
	if u.WelcomeMode != nil {
		cfg.WelcomeMode = *u.WelcomeMode
	}
	if u.WelcomeTemplates != nil {
		cfg.WelcomeTemplates = append([]string(nil), (*u.WelcomeTemplates)...)
	}
	if u.WelcomeLLMCooldownSeconds != nil {
		cfg.WelcomeLLMCooldownSeconds = *u.WelcomeLLMCooldownSeconds
	}
	return cfg
}

func parseWelcomeSettingsUpdate(input map[string]any) (BotSettingsUpdate, error) {
	var update BotSettingsUpdate
	if raw, exists := input["welcome_enabled"]; exists {
		enabled, ok := raw.(bool)
		if !ok {
			return update, fmt.Errorf("welcome_enabled 必须是布尔值")
		}
		update.WelcomeEnabled = &enabled
	}
	if raw, exists := input["welcome_message"]; exists {
		text, ok := raw.(string)
		if !ok {
			return update, fmt.Errorf("welcome_message 必须是文本")
		}
		text = strings.TrimSpace(text)
		update.WelcomeMessage = &text
	}
	if raw, exists := input["welcome_mode"]; exists {
		text, ok := raw.(string)
		mode := WelcomeMode(strings.TrimSpace(text))
		if !ok || (mode != WelcomeModeFixed && mode != WelcomeModeTemplate && mode != WelcomeModeLLM) {
			return update, fmt.Errorf("welcome_mode 必须是 fixed、template 或 llm")
		}
		update.WelcomeMode = &mode
	}
	if raw, exists := input["welcome_templates"]; exists {
		templates, err := toolStringValues(raw)
		if err != nil || raw == nil {
			return update, fmt.Errorf("welcome_templates 必须是文本数组")
		}
		templates = cleanStrings(templates)
		if len(templates) > 50 {
			return update, fmt.Errorf("欢迎词模板最多 50 条")
		}
		for _, template := range templates {
			if len([]rune(template)) > 200 {
				return update, fmt.Errorf("欢迎词模板不能超过 200 字")
			}
		}
		update.WelcomeTemplates = &templates
	}
	if raw, exists := input["welcome_llm_cooldown_seconds"]; exists {
		seconds, err := groupToolInteger(raw)
		if err != nil || seconds < 0 || seconds > 86400 {
			return update, fmt.Errorf("welcome_llm_cooldown_seconds 必须为 0–86400 的整数")
		}
		update.WelcomeLLMCooldownSeconds = &seconds
	}
	return update, nil
}

type welcomeSettings struct {
	Enabled            bool        `json:"enabled"`
	Message            string      `json:"message"`
	Mode               WelcomeMode `json:"mode"`
	Templates          []string    `json:"templates"`
	LLMCooldownSeconds int         `json:"llm_cooldown_seconds"`
}

func welcomeSettingsFromConfig(cfg BotConfig) welcomeSettings {
	return welcomeSettings{Enabled: cfg.WelcomeEnabled, Message: cfg.WelcomeMessage, Mode: cfg.WelcomeMode,
		Templates: append([]string(nil), cfg.WelcomeTemplates...), LLMCooldownSeconds: cfg.WelcomeLLMCooldownSeconds}
}
