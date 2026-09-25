package assistant

import (
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
