package assistant

import (
	"fmt"
	"strings"
)

// NormalizeGroupConfigFields 是群配置里不依赖存储和运行时的校验，WebUI 群设置和
// bot_config 的 fields 共用，保证两个入口接受的值一致。
func NormalizeGroupConfigFields(cfg GroupConfig) (GroupConfig, error) {
	cfg.GroupTriggers = cleanStrings(cfg.GroupTriggers)
	cfg.WelcomeMessage = strings.TrimSpace(cfg.WelcomeMessage)
	cfg.WelcomeTemplates = cleanStrings(cfg.WelcomeTemplates)
	if len(cfg.WelcomeTemplates) > 50 {
		return GroupConfig{}, fmt.Errorf("欢迎词模板最多 50 条")
	}
	for _, template := range cfg.WelcomeTemplates {
		if len([]rune(template)) > 200 {
			return GroupConfig{}, fmt.Errorf("欢迎词模板不能超过 200 字")
		}
	}
	if cfg.WelcomeLLMCooldownSeconds < 0 {
		cfg.WelcomeLLMCooldownSeconds = 0
	}
	if cfg.WelcomeLLMCooldownSeconds > 24*60*60 {
		return GroupConfig{}, fmt.Errorf("欢迎词 LLM 冷却不能超过 86400 秒")
	}
	cfg.ReplyAccountSafetyAuditPrompt = strings.TrimSpace(cfg.ReplyAccountSafetyAuditPrompt)
	if len([]rune(cfg.ReplyAccountSafetyAuditPrompt)) > AccountSafetyRulesMaxRunes {
		return GroupConfig{}, fmt.Errorf("账号安全审核规则不能超过 8000 字")
	}
	cfg.ProactiveReplyExtraCriteria = strings.TrimSpace(cfg.ProactiveReplyExtraCriteria)
	if len([]rune(cfg.ProactiveReplyExtraCriteria)) > ProactiveReplyExtraCriteriaMaxRunes {
		return GroupConfig{}, fmt.Errorf("接话补充判据不能超过 %d 字", ProactiveReplyExtraCriteriaMaxRunes)
	}
	if cfg.Governance != nil {
		if err := cfg.Governance.Validate(); err != nil {
			return GroupConfig{}, err
		}
	}
	return cfg, nil
}
