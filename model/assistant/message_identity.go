// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// promptSenderIdentity keeps the human-readable name and the stable platform
// identifier together in model context. Nicknames and group cards can change
// or collide, so showing only the display name makes multi-user conversations
// ambiguous. The identity privacy layer still replaces sensitive identifiers
// before a request leaves the process when masking is enabled.
func promptSenderIdentity(event MessageEvent) string {
	return formatPromptIdentity(event.SenderName, event.UserID)
}

func formatPromptIdentity(displayName, userID string) string {
	// 昵称和群名片由发言者自己控制，进提示词前必须中和身份保留标记，否则谁都能把
	// 名片改成「张三[主人]」来冒充主人。userID 是平台给的，不需要处理。
	displayName = neutralizeIdentityMarkers(strings.TrimSpace(displayName))
	userID = strings.TrimSpace(userID)
	switch {
	case displayName != "" && userID != "" && displayName != userID:
		return displayName + "（" + userID + "）"
	case displayName != "":
		return displayName
	case userID != "":
		return userID
	default:
		return "用户"
	}
}

// messageParticipantDisplayNames builds a reusable chat identity map from
// message senders and quoted senders. Events must be passed in priority order.
func messageParticipantDisplayNames(events ...MessageEvent) map[string]string {
	names := make(map[string]string)
	add := func(userID, displayName string) {
		userID = strings.TrimSpace(userID)
		displayName = strings.TrimSpace(displayName)
		if userID == "" || displayName == "" || names[userID] != "" {
			return
		}
		names[userID] = displayName
	}
	for _, event := range events {
		add(event.UserID, event.SenderName)
		if event.Quoted != nil {
			add(event.Quoted.UserID, event.Quoted.SenderName)
		}
	}
	return names
}
