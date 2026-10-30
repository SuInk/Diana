package webui

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/SuInk/diana/model/assistant"
)

// 聊天里 bot_config 的 fields 经这里保存，和 WebUI 设置页走同一套校验与重连，
// 不会出现「聊天里能存进去、WebUI 里却存不了」的值。

var _ assistant.ConfigFieldsSaver = (*BotHandler)(nil)

// SaveBotFields 在最新的机器人配置上应用补丁，校验后热应用并落盘；连接参数变化时重连。
func (h *BotHandler) SaveBotFields(ctx context.Context, profileID string, patch func(assistant.BotConfig) (assistant.BotConfig, error)) (assistant.BotConfig, error) {
	set := h.profiles.Profiles().WithDefaults()
	index := -1
	for i, cfg := range set.Profiles {
		if cfg.ID == profileID {
			index = i
			break
		}
	}
	if index < 0 {
		return assistant.BotConfig{}, fmt.Errorf("机器人已不存在")
	}
	existing := set.Profiles[index]
	next, err := patch(existing)
	if err != nil {
		return assistant.BotConfig{}, err
	}
	next.ID = existing.ID
	if err := set.ValidateIndependentConnection(next); err != nil {
		return assistant.BotConfig{}, err
	}
	if next.OneBotAccessToken != existing.OneBotAccessToken {
		if err := validateTokenLength("onebot_access_token", next.OneBotAccessToken); err != nil {
			return assistant.BotConfig{}, err
		}
	}
	if err := next.Validate(); err != nil {
		return assistant.BotConfig{}, err
	}
	if err := h.validateSearchOverride(next.WebSearchOverride); err != nil {
		return assistant.BotConfig{}, err
	}
	set.Profiles = slices.Clone(set.Profiles)
	set.Profiles[index] = next
	if err := h.applyProfileSet(set); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		return assistant.BotConfig{}, err
	}
	if err := h.profiles.SaveProfiles(set); err != nil {
		return assistant.BotConfig{}, fmt.Errorf("设置已生效，但保存失败，重启后会恢复原状：%w", err)
	}
	saved, _ := set.ConfigForProfile(next.ID)
	recordOperation(ctx, h.logs, "config_save", "机器人设置已在聊天里修改", botLogTarget(saved), botLogMetadata(saved))
	return saved, nil
}

// SaveGroupFields 复用 WebUI 群设置的校验；群管理员改扩展权限受机器人上限约束，
// 机器人标记只有主人能改，与 WebUI 群管理页一致。
func (h *BotHandler) SaveGroupFields(_ context.Context, cfg assistant.GroupConfig, previous assistant.GroupConfig, base assistant.BotConfig, byOwner bool) (assistant.GroupConfig, error) {
	if !byOwner && !slices.Equal(cfg.MarkedBotIDs, previous.MarkedBotIDs) {
		return assistant.GroupConfig{}, fmt.Errorf("只有机器人主人可以修改机器人标记")
	}
	cfg.BotProfileID = base.ID
	sanitized, err := h.sanitizeGroupConfigPayload(cfg, cfg.GroupID)
	if err != nil {
		return assistant.GroupConfig{}, err
	}
	sanitized.BotProfileID = base.ID
	if !byOwner {
		if err := h.groupExtensionAccessWithinBotLimits(base.ID, previous.ExtensionAccess, sanitized.ExtensionAccess); err != nil {
			return assistant.GroupConfig{}, err
		}
	}
	return h.groupConfigs.SaveGroupConfig(sanitized, base)
}
