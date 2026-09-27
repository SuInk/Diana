package assistant

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// TelegramOwnerUsername accepts a handle with or without @. Numeric account
// identifiers remain IDs, never usernames. Display names are not accepted.
func TelegramOwnerUsername(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if len(value) < 5 || len(value) > 32 || allASCIIDigits(value) {
		return ""
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_') {
			return ""
		}
	}
	return strings.ToLower(value)
}

// OwnerIDForEvent resolves only the authenticated sender of this event. It
// neither rewrites the saved owner setting nor changes message/routing IDs.
func (cfg BotConfig) OwnerIDForEvent(event MessageEvent) string {
	owner := strings.TrimSpace(cfg.OwnerID)
	if cfg.Platform != PlatformTelegram || event.Platform != PlatformTelegram || event.SenderIsBot || event.Outbound || (cfg.ID != "" && event.ProfileID != "" && cfg.ID != event.ProfileID) {
		return owner
	}
	handle := TelegramOwnerUsername(owner)
	id, err := strconv.ParseInt(event.UserID, 10, 64)
	if handle != "" && err == nil && id > 0 && handle == TelegramOwnerUsername(event.SenderUsername) {
		return event.UserID
	}
	return owner
}

func (cfg BotConfig) IsOwnerEvent(event MessageEvent) bool {
	owner := cfg.OwnerIDForEvent(event)
	return owner != "" && owner == strings.TrimSpace(event.UserID)
}

// callerIdentityForEvent 是交给 MCP 和本地命令的真实调用者。它不经过模型，隐私代理
// 开着也照样是真实 ID：代理只挡模型，不挡主人自己配的扩展。
func callerIdentityForEvent(cfg BotConfig, event MessageEvent) agent.CallerIdentity {
	identity := agent.CallerIdentity{
		Platform:  NormalizePlatformID(firstNonEmpty(event.Platform, cfg.Platform)),
		BotID:     strings.TrimSpace(firstNonEmpty(event.SelfID, cfg.BotAccount)),
		UserID:    strings.TrimSpace(event.UserID),
		MessageID: strings.TrimSpace(event.MessageID),
		ChatType:  string(EventKindPrivate),
		IsOwner:   cfg.IsOwnerEvent(event),
	}
	if event.Kind == EventKindGroup {
		identity.ChatType = string(EventKindGroup)
		identity.GroupID = strings.TrimSpace(event.GroupID)
	}
	return identity
}

func relationshipPolicyForEvent(cfg BotConfig, profile UserMemoryProfile, event MessageEvent) RelationshipPolicy {
	cfg.OwnerID = cfg.OwnerIDForEvent(event)
	return RelationshipPolicyForConfig(cfg, profile, event.UserID)
}

// rememberTelegramOwnerID 在主人配成 @用户名时，记下主人发消息时带出的数字 ID。
// Bot API 不能按用户名私聊，给主人发通知只能用数字 ID，而它只有主人开口后才知道。
func (r *Runtime) rememberTelegramOwnerID(event MessageEvent) {
	if r == nil || NormalizePlatformID(event.Platform) != PlatformTelegram {
		return
	}
	cfg := r.effectiveConfigForEvent(event)
	if TelegramOwnerUsername(cfg.OwnerID) == "" || !cfg.IsOwnerEvent(event) || !allASCIIDigits(strings.TrimSpace(event.UserID)) {
		return
	}
	r.telegramOwnerIDs.Store(firstNonEmpty(strings.TrimSpace(event.ProfileID), cfg.ID), strings.TrimSpace(event.UserID))
}

// ownerDeliveryID 返回能私聊到主人的账号 ID。Telegram 主人配成 @用户名时，先用记住的
// 数字 ID，再从本机聊天记录里找主人说过的话；都没有就说清楚为什么发不出去。
func (r *Runtime) ownerDeliveryID(event MessageEvent, cfg BotConfig) (string, error) {
	owner := strings.TrimSpace(cfg.OwnerID)
	if owner == "" {
		return "", fmt.Errorf("没有配置机器人主人")
	}
	handle := TelegramOwnerUsername(owner)
	if NormalizePlatformID(firstNonEmpty(event.Platform, cfg.Platform)) != PlatformTelegram || handle == "" {
		return owner, nil
	}
	if id, ok := r.telegramOwnerIDs.Load(firstNonEmpty(strings.TrimSpace(event.ProfileID), cfg.ID)); ok {
		return id.(string), nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, history := range r.history {
		for i := len(history) - 1; i >= 0; i-- {
			item := history[i]
			if item.Outbound || item.SenderIsBot || TelegramOwnerUsername(item.SenderUsername) != handle || !allASCIIDigits(strings.TrimSpace(item.UserID)) {
				continue
			}
			if strings.TrimSpace(event.ProfileID) != "" && strings.TrimSpace(item.ProfileID) != "" && item.ProfileID != event.ProfileID {
				continue
			}
			return strings.TrimSpace(item.UserID), nil
		}
	}
	return "", fmt.Errorf("主人配置的是 Telegram 用户名 @%s，Bot API 不能按用户名私聊；请主人先给机器人发一条消息，或把主人改成数字 ID", handle)
}
