// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// InboundEventRecall 是这条消息本身，或这一轮发出去的某条回复后来被撤回的记录。
type InboundEventRecall struct {
	// MessageID 是被撤回的那条消息号：等于事件自己的 MessageID 时撤的是收到的这条
	// 消息，否则是 OutboundMessageID 里的一项。
	MessageID    string    `json:"message_id"`
	At           time.Time `json:"at"`
	OperatorID   string    `json:"operator_id,omitempty"`
	OperatorName string    `json:"operator_name,omitempty"`
	OperatorRole string    `json:"operator_role,omitempty"`
	// SelfRecall 表示是机器人自己撤回的（发现说错了调 recall），不是被别人撤。
	SelfRecall bool `json:"self_recall,omitempty"`
}

// attachInboundEventRecalls 把撤回通知挂到被撤那条消息所在的事件上。
//
// 撤回通知本身也是一行事件，但那一行只写得出「某某撤回了 Diana 的消息」，撤的
// 是哪句回复、回的是谁，得拿消息号回头对。群友撤回自己的消息也一样多出一行，
// 原话还得往下翻。挂到原消息或原回复上，控制台就能把两行合成一行。撤回通知只按
// 消息号查 message_events 的 (message_id, kind) 索引，一页最多几百个号，不需要扫表。
func (s *SQLiteStore) attachInboundEventRecalls(ctx context.Context, events []InboundEventDetail) error {
	defer s.observeStorage(ctx, "attachInboundEventRecalls", "read")()
	owners := map[string][]int{}
	for index, event := range events {
		// 撤回通知那一行自己也带着被撤的消息号，别把撤回挂回它自己身上。
		if event.Kind == string(assistant.EventKindNotice) {
			continue
		}
		if id := strings.TrimSpace(event.MessageID); id != "" {
			owners[id] = append(owners[id], index)
		}
		for _, id := range strings.Split(event.OutboundMessageID, ",") {
			if id = strings.TrimSpace(id); id != "" {
				owners[id] = append(owners[id], index)
			}
		}
	}
	if len(owners) == 0 {
		return nil
	}
	args := make([]any, 0, len(owners))
	for id := range owners {
		args = append(args, id)
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT COALESCE(message_id, ''), event_time, COALESCE(group_id, ''), COALESCE(payload, '')
FROM message_events
WHERE kind = 'notice' AND message_id IN (`+placeholders(len(args))+`)
ORDER BY event_time ASC, created_at ASC
`, args...)
	if err != nil {
		return fmt.Errorf("load inbound event recalls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var messageID, groupID, payload string
		var eventTime int64
		if err := rows.Scan(&messageID, &eventTime, &groupID, &payload); err != nil {
			return fmt.Errorf("scan inbound event recall: %w", err)
		}
		source, ok := decodeInboundEventMessage(payload, "")
		if !ok || (source.SubType != "group_recall" && source.SubType != "friend_recall") {
			continue
		}
		recall := InboundEventRecall{
			MessageID:    strings.TrimSpace(messageID),
			At:           time.Unix(eventTime, 0),
			OperatorID:   strings.TrimSpace(source.OperatorID),
			OperatorName: strings.TrimSpace(source.OperatorName),
			OperatorRole: strings.TrimSpace(source.OperatorRole),
		}
		recall.SelfRecall = recall.OperatorID != "" && recall.OperatorID == strings.TrimSpace(source.UserID)
		for _, index := range owners[recall.MessageID] {
			event := &events[index]
			// 消息号只在一个会话里唯一：别的群里碰巧同号的撤回不能挂过来。
			if strings.TrimSpace(event.GroupID) != strings.TrimSpace(groupID) {
				continue
			}
			if profileID := strings.TrimSpace(source.ProfileID); profileID != "" && event.ProfileID != "" && profileID != event.ProfileID {
				continue
			}
			// 断线回补和实时通知可能各记一条同一次撤回，只留先到的那条。
			key := event.ID + "\x00" + recall.MessageID
			if seen[key] {
				continue
			}
			seen[key] = true
			event.Recalls = append(event.Recalls, recall)
		}
	}
	return rows.Err()
}
