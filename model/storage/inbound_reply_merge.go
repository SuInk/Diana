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

func (s *SQLiteStore) RecordInboundEventReplyMerge(ctx context.Context, event assistant.MessageEvent, rootTurnID string) error {
	if s == nil || s.db == nil || strings.TrimSpace(event.MessageID) == "" || strings.TrimSpace(rootTurnID) == "" {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events SET superseded_by = ?, updated_at = ?
WHERE id = (
  SELECT id FROM inbound_events
  WHERE message_id = ? AND kind = ?
    AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
  ORDER BY created_at DESC, id DESC LIMIT 1
)
`, strings.TrimSpace(rootTurnID), now, strings.TrimSpace(event.MessageID), strings.TrimSpace(string(event.Kind)), strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID))
	if err != nil {
		return fmt.Errorf("record inbound event reply merge: %w", err)
	}
	return nil
}
