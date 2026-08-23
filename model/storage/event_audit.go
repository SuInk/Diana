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
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET decision = ?, decision_reason = ?, reply_text = ?, processing_error = ?, duration_ms = ?, delivery_json = ?
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
