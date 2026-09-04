// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const sessionThreadPromptPrefix = "【当前会话进行状态，用于接上正在聊的事；不要复述它，也不要直接回复它】\n"

// recordTemporaryMemoryContext records only the short-lived state that really
// survives the final token budget. Ordinary recent chat history stays in the
// message event table and is not duplicated into app_logs on every turn.
func (r *Runtime) recordTemporaryMemoryContext(ctx context.Context, event MessageEvent, cfg BotConfig, messages []llm.Message, preload *promptContextPreload) {
	writer := r.appLogWriter()
	if writer == nil || preload == nil || strings.TrimSpace(event.MessageID) == "" {
		return
	}
	fitted := llm.FitMessagesToContextBudget(messages, r.promptContextWindowTokens(event, cfg), llm.DefaultMaxOutputTokens)
	privateStateInjected := false
	sessionThread := ""
	for _, message := range fitted {
		content := strings.TrimSpace(message.Content)
		switch {
		case strings.HasPrefix(content, privateThreadStateMarker):
			privateStateInjected = true
		case strings.HasPrefix(content, strings.TrimSpace(sessionThreadPromptPrefix)):
			sessionThread = strings.TrimSpace(strings.TrimPrefix(content, strings.TrimSpace(sessionThreadPromptPrefix)))
		}
	}
	items := make([]map[string]any, 0, len(preload.threadStates)+1)
	if privateStateInjected {
		for _, state := range preload.threadStates {
			var payload any
			if json.Unmarshal(state.State, &payload) != nil {
				payload = string(state.State)
			}
			items = append(items, map[string]any{
				"id": state.ID, "kind": "private_thread_state", "task_kind": state.TaskKind,
				"scope": state.Scope, "content": payload, "version": state.Version, "expires_at": state.ExpiresAt,
				"source_message_id": state.SourceMessageID,
			})
		}
	}
	if sessionThread != "" {
		item := map[string]any{"kind": "session_thread", "content": sessionThread}
		if memory := preload.sessionThreadMemory; memory != nil {
			item["id"] = memory.ID
			item["topic"] = memory.Topic
			item["source_message_id"] = memory.SourceMessageID
			item["expires_at"] = memory.ExpiresAt
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind: applog.KindDebug, Level: applog.LevelInfo, Action: "diana.memory.temporary",
		Message: "临时记忆已进入本轮上下文", Target: event.MessageID,
		Metadata: map[string]any{
			"platform": event.Platform, "profile_id": event.ProfileID, "group_id": event.GroupID,
			"user_id": event.UserID, "message_id": event.MessageID, "memories": items,
		},
	})
}
