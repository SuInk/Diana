package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// RecordInboundEventDelivery advances the durable delivery state for the
// source inbound event. Stages are monotonic, so late retry callbacks cannot
// downgrade an acknowledged or echoed message.
func (s *SQLiteStore) RecordInboundEventDelivery(ctx context.Context, event assistant.MessageEvent, stage assistant.OutboundDeliveryStage, outboundMessageID, detail string) error {
	if s == nil || s.db == nil || strings.TrimSpace(event.MessageID) == "" {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	outboundMessageID = strings.TrimSpace(outboundMessageID)
	detail = strings.TrimSpace(detail)
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET delivery_stage = CASE
      WHEN ? = 'failed' AND COALESCE(delivery_stage, '') IN ('acknowledged', 'echo_persisted') THEN delivery_stage
      WHEN ? = 'send_attempted' AND COALESCE(delivery_stage, '') IN ('acknowledged', 'echo_persisted') THEN delivery_stage
      ELSE ?
    END,
    outbound_message_id = CASE WHEN ? != '' THEN ? ELSE outbound_message_id END,
    reply_generated_at = CASE WHEN ? = 'generated' AND reply_generated_at IS NULL THEN ? ELSE reply_generated_at END,
    send_attempted_at = CASE WHEN ? = 'send_attempted' AND send_attempted_at IS NULL THEN ? ELSE send_attempted_at END,
    send_acked_at = CASE WHEN ? = 'acknowledged' THEN ? ELSE send_acked_at END,
    delivery_error = CASE WHEN ? = 'failed' THEN ? WHEN ? IN ('acknowledged', 'echo_persisted') THEN NULL ELSE delivery_error END,
    updated_at = ?
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
		string(stage), string(stage), string(stage),
		outboundMessageID, outboundMessageID,
		string(stage), now,
		string(stage), now,
		string(stage), now,
		string(stage), detail, string(stage), now,
		strings.TrimSpace(event.MessageID), strings.TrimSpace(string(event.Kind)),
		strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID),
	)
	if err != nil {
		return fmt.Errorf("record inbound delivery stage: %w", err)
	}
	return nil
}

// RecordInboundEventSelfEcho links a real OneBot self-message echo to the
// previously acknowledged outbound message.
func (s *SQLiteStore) RecordInboundEventSelfEcho(ctx context.Context, outboundMessageID string, observedAt time.Time) error {
	if s == nil || s.db == nil || strings.TrimSpace(outboundMessageID) == "" {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	now := time.Now().UTC().UnixNano()
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET delivery_stage = 'echo_persisted', self_echo_at = ?, delivery_error = NULL, updated_at = ?
WHERE outbound_message_id = ?
`, observedAt.UTC().UnixNano(), now, strings.TrimSpace(outboundMessageID))
	if err != nil {
		return fmt.Errorf("record inbound self echo: %w", err)
	}
	return nil
}
