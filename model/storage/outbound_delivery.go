// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
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
    outbound_message_id = CASE
      WHEN ? = '' THEN outbound_message_id
      WHEN COALESCE(outbound_message_id, '') = '' THEN ?
      WHEN instr(',' || outbound_message_id || ',', ',' || ? || ',') > 0 THEN outbound_message_id
      ELSE outbound_message_id || ',' || ?
    END,
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
		outboundMessageID, outboundMessageID, outboundMessageID, outboundMessageID,
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
WHERE ',' || outbound_message_id || ',' LIKE '%,' || ? || ',%'
`, observedAt.UTC().UnixNano(), now, strings.TrimSpace(outboundMessageID))
	if err != nil {
		return fmt.Errorf("record inbound self echo: %w", err)
	}
	return nil
}

// MarkLegacyInboundNamespaceDuplicates prevents already-persisted duplicate
// backfill rows from being claimed after an upgrade. It keeps every audit row
// and only terminalizes duplicate pending/processing copies.
func (s *SQLiteStore) MarkLegacyInboundNamespaceDuplicates(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, payload, status
FROM inbound_events
WHERE NULLIF(TRIM(message_id), '') IS NOT NULL
ORDER BY event_time ASC, created_at ASC, id ASC
`)
	if err != nil {
		return 0, fmt.Errorf("list legacy inbound duplicates: %w", err)
	}
	type candidate struct {
		id     string
		event  assistant.MessageEvent
		status string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		var payload string
		if err := rows.Scan(&item.id, &payload, &item.status); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan legacy inbound duplicate: %w", err)
		}
		if jsonErr := json.Unmarshal([]byte(payload), &item.event); jsonErr == nil {
			candidates = append(candidates, item)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	groups := map[string][]candidate{}
	for _, item := range candidates {
		key := inboundTransportKey(item.event)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], item)
	}
	var duplicates []string
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		hasDone := false
		for _, item := range group {
			if item.status == inboundStatusDone {
				hasDone = true
				break
			}
		}
		keptLive := false
		for _, item := range group {
			if item.status == inboundStatusDone {
				continue
			}
			if !hasDone && !keptLive {
				keptLive = true
				continue
			}
			duplicates = append(duplicates, item.id)
		}
	}
	if len(duplicates) == 0 {
		return 0, nil
	}
	now := time.Now().UTC().UnixNano()
	var updated int64
	for _, id := range duplicates {
		result, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET status = 'done', outcome = 'ignored_duplicate', decision = 'not_replied',
    decision_reason = '同一平台消息已在其他会话命名空间入队，本条重复回补已忽略',
    lease_owner = NULL, lease_until = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status IN ('pending', 'processing')
`, now, now, id)
		if err != nil {
			return updated, fmt.Errorf("mark legacy inbound duplicate %q: %w", id, err)
		}
		count, _ := result.RowsAffected()
		updated += count
	}
	return updated, nil
}

func inboundTransportKey(event assistant.MessageEvent) string {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" {
		return ""
	}
	return strings.Join([]string{
		assistant.NormalizePlatformID(event.Platform), strings.TrimSpace(event.SelfID),
		strings.TrimSpace(string(event.Kind)), strings.TrimSpace(event.GroupID),
		strings.TrimSpace(event.UserID), messageID,
	}, "\x00")
}
