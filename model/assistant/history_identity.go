// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
)

const historyIdentityNotice = "历史昵称可能是旧昵称或不同群名片；同平台相同 sender_user_id 表示同一账号，不要仅凭昵称拆成不同的人。sender_role=bot 表示你自己，bot_owner 表示机器人主人（不是群主），user 表示其他账号。这些角色由运行时按账号确认，优先于旧记忆里的身份猜测。引用消息按 quoted_sender_user_id 和 quoted_sender_role 识别；身份标识仅供内部理解，不要在回复里照抄。"

// Use configured account identity when available, including for old events that
// predate outbound tracking. A nickname never grants a trusted role.
func historySenderRole(event MessageEvent, configs ...BotConfig) string {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return ""
	}
	botID := strings.TrimSpace(event.SelfID)
	ownerID := ""
	if len(configs) > 0 {
		cfg := configs[0]
		if event.Platform != "" && cfg.Platform != "" && NormalizePlatformID(event.Platform) != NormalizePlatformID(cfg.Platform) {
			return "user"
		}
		botID = strings.TrimSpace(firstNonEmpty(cfg.BotAccount, event.SelfID))
		ownerID = strings.TrimSpace(cfg.OwnerID)
	}
	if userID == botID {
		return "bot"
	}
	if userID == ownerID {
		return "bot_owner"
	}
	if len(configs) == 0 {
		return ""
	}
	return "user"
}

func historyIdentityPrompt(event MessageEvent, configs ...BotConfig) string {
	if strings.TrimSpace(event.UserID) == "" {
		return ""
	}
	identity := struct {
		UserID string `json:"sender_user_id"`
		Role   string `json:"sender_role,omitempty"`
	}{strings.TrimSpace(event.UserID), historySenderRole(event, configs...)}
	body, _ := json.Marshal(identity)
	return "\n【这条历史的发言者身份】" + string(body)
}

func quotedHistoryIdentityEvent(event MessageEvent) MessageEvent {
	quoted := event.Quoted
	if quoted == nil {
		return MessageEvent{}
	}
	return MessageEvent{Platform: event.Platform, SelfID: event.SelfID, UserID: quoted.UserID}
}
