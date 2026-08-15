package storage

import (
	"context"
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
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET decision = ?, decision_reason = ?, reply_text = ?, processing_error = ?, duration_ms = ?
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
