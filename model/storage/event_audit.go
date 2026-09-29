// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// RecordInboundEventAudit persists the runtime's final human-readable decision
// and reply result on the durable inbound queue row.
func (s *SQLiteStore) RecordInboundEventAudit(ctx context.Context, event assistant.EventRecord) error {
	if s == nil || s.db == nil || strings.TrimSpace(event.MessageID) == "" {
		return nil
	}
	// 没发出任何东西时存 NULL，免得每条事件都躺一个 {} 占位。
	var deliveryJSON any
	if !event.Delivery.Empty() {
		encoded, err := json.Marshal(event.Delivery)
		if err != nil {
			return fmt.Errorf("encode inbound event delivery: %w", err)
		}
		deliveryJSON = string(encoded)
	}
	// reply_wait_ms 是进队列到这一轮回复开始之间等了多久（排队、冷却、回复判断都在
	// 里面）。只有真的跑过回复轮次的记录才有这个起点：Duration 为 0 的是没进回复
	// 轮次就收尾的判断记录，它的 At 不是回复开始时刻。主人手动重试的那一行
	// created_at 还是当初入队的时刻，拿来减会算出几小时的「排队」，不记。
	var replyStartedAt int64
	if event.Duration > 0 && !event.At.IsZero() {
		replyStartedAt = event.At.UTC().UnixNano()
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET decision = ?, decision_reason = ?, reply_text = ?, processing_error = ?, duration_ms = ?, delivery_json = ?,
    reply_wait_ms = CASE
      WHEN ? > 0 AND ? > 0 AND COALESCE(CASE WHEN json_valid(payload) THEN json_extract(payload, '$.manual_retry') END, 0) <> 1
        THEN MAX(0, (? - created_at) / 1000000)
      ELSE NULL
    END
WHERE id = (
  SELECT id
  FROM inbound_events
  WHERE message_id = ?
    AND kind = ?
    AND COALESCE(group_id, '') = ?
    AND COALESCE(user_id, '') = ?
  ORDER BY created_at DESC, id DESC
  LIMIT 1
)
`,
		strings.TrimSpace(event.Decision),
		strings.TrimSpace(event.Reason),
		event.Reply,
		strings.TrimSpace(event.Error),
		event.Duration,
		deliveryJSON,
		event.Duration, replyStartedAt, replyStartedAt,
		strings.TrimSpace(event.MessageID),
		strings.TrimSpace(string(event.Kind)),
		strings.TrimSpace(event.GroupID),
		strings.TrimSpace(event.UserID),
	)
	if err != nil {
		return fmt.Errorf("record inbound event audit: %w", err)
	}
	return nil
}
