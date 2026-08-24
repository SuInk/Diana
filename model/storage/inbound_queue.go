// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

const (
	inboundStatusPending    = "pending"
	inboundStatusProcessing = "processing"
	inboundStatusDone       = "done"
)

// EnqueueInboundEvent atomically records message history and makes a new event
// available to the durable worker queue. Existing history rows remain
// deduplicated so pre-queue chat history is never replayed after an upgrade.
func (s *SQLiteStore) EnqueueInboundEvent(ctx context.Context, session string, event assistant.MessageEvent, priorities ...int) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("enqueue inbound event: sqlite store is not configured")
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return "", false, errors.New("enqueue inbound event: session is required")
	}

	id, err := stableInboundEventID(session, event)
	if err != nil {
		return "", false, err
	}
	now := time.Now().UTC()
	priority := inboundPriorityValue(priorities)
	if event.Time <= 0 {
		event.Time = now.Unix()
	}
	event, err = s.persistInboundVoiceBlobs(ctx, event)
	if err != nil {
		return "", false, err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return "", false, fmt.Errorf("encode inbound event: %w", err)
	}
	text := strings.TrimSpace(assistant.PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin inbound enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if existingID, found, findErr := findDuplicateInboundHistory(ctx, tx, event); findErr != nil {
		return "", false, findErr
	} else if found {
		if _, err := tx.ExecContext(ctx, `
UPDATE inbound_events
SET priority = CASE WHEN priority < ? THEN ? ELSE priority END,
    updated_at = ?
WHERE id = ? AND status = ?
		`, priority, priority, now.UnixNano(), existingID, inboundStatusPending); err != nil {
			return "", false, fmt.Errorf("refresh duplicate inbound priority: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return "", false, fmt.Errorf("commit duplicate inbound event: %w", err)
		}
		return existingID, false, nil
	}

	createdAt := now.Format(time.RFC3339Nano)
	historyResult, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, session, string(event.Kind), event.GroupID, event.UserID, event.MessageID, event.SenderName, event.Time, text, string(payload), createdAt)
	if err != nil {
		return "", false, fmt.Errorf("persist inbound message history: %w", err)
	}
	historyInserted, err := historyResult.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("inspect inbound message history: %w", err)
	}
	if historyInserted == 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE message_events
SET kind = ?, group_id = ?, user_id = ?, message_id = ?, sender_name = ?,
    event_time = ?, text = ?, payload = ?, created_at = ?
WHERE id = ?
		`, string(event.Kind), event.GroupID, event.UserID, event.MessageID, event.SenderName, event.Time, text, string(payload), createdAt, id); err != nil {
			return "", false, fmt.Errorf("refresh inbound message history: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE inbound_events
SET priority = CASE WHEN priority < ? THEN ? ELSE priority END,
    payload = ?, updated_at = ?
WHERE id = ? AND status = ?
		`, priority, priority, string(payload), now.UnixNano(), id, inboundStatusPending); err != nil {
			return "", false, fmt.Errorf("refresh inbound queue priority: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return "", false, fmt.Errorf("commit duplicate inbound history: %w", err)
		}
		return id, false, nil
	}

	nowNanos := now.UnixNano()
	availableAt := inboundInitialAvailableAt(event, now)
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO inbound_events (
  id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload,
  priority, status, attempts, available_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
`, id, session, string(event.Kind), strings.TrimSpace(event.ProfileID), event.GroupID, event.UserID, event.MessageID, event.Time, string(payload), priority, inboundStatusPending, availableAt.UnixNano(), nowNanos, nowNanos)
	if err != nil {
		return "", false, fmt.Errorf("enqueue inbound event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("inspect inbound enqueue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit inbound enqueue: %w", err)
	}
	return id, inserted > 0, nil
}

// RecordNoticeEvent adds a terminal audit row for a notice that must be visible
// in the event timeline without sending it through the reply worker queue.
func (s *SQLiteStore) RecordNoticeEvent(ctx context.Context, session string, event assistant.MessageEvent) error {
	if s == nil || s.db == nil {
		return errors.New("record notice event: sqlite store is not configured")
	}
	session = strings.TrimSpace(session)
	if session == "" || event.Kind != assistant.EventKindNotice {
		return errors.New("record notice event: notice session is required")
	}
	id, err := stableInboundEventID(session, event)
	if err != nil {
		return err
	}
	if event.Time <= 0 {
		event.Time = time.Now().Unix()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notice event: %w", err)
	}
	now := time.Now().UTC().UnixNano()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO inbound_events (
  id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload,
  priority, status, attempts, available_at, outcome, decision, decision_reason,
  created_at, updated_at, completed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 0, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  payload = excluded.payload,
  outcome = excluded.outcome,
  decision = excluded.decision,
  decision_reason = excluded.decision_reason,
  updated_at = excluded.updated_at,
  completed_at = excluded.completed_at
`, id, session, string(event.Kind), strings.TrimSpace(event.ProfileID), event.GroupID, event.UserID, event.MessageID, event.Time, string(payload),
		inboundStatusDone, now, "notice_"+strings.TrimSpace(event.SubType), "notice", "已记录平台通知", now, now, now)
	if err != nil {
		return fmt.Errorf("record notice event: %w", err)
	}
	return nil
}

func findDuplicateInboundHistory(ctx context.Context, tx *sql.Tx, event assistant.MessageEvent) (string, bool, error) {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" {
		return "", false, nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, payload
FROM message_events
WHERE message_id = ?
  AND kind = ?
  AND COALESCE(group_id, '') = ?
  AND COALESCE(user_id, '') = ?
ORDER BY event_time DESC, created_at DESC, id DESC
`, messageID, string(event.Kind), strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID))
	if err != nil {
		return "", false, fmt.Errorf("find duplicate inbound history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			return "", false, fmt.Errorf("scan duplicate inbound history: %w", err)
		}
		var stored assistant.MessageEvent
		if json.Unmarshal([]byte(payload), &stored) != nil || !sameInboundTransport(event, stored) {
			continue
		}
		return id, true, nil
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("iterate duplicate inbound history: %w", err)
	}
	return "", false, nil
}

func sameInboundTransport(current, stored assistant.MessageEvent) bool {
	currentSelfID := strings.TrimSpace(current.SelfID)
	storedSelfID := strings.TrimSpace(stored.SelfID)
	if currentSelfID != "" && storedSelfID != "" && currentSelfID != storedSelfID {
		return false
	}
	currentPlatform := strings.TrimSpace(current.Platform)
	storedPlatform := strings.TrimSpace(stored.Platform)
	if currentPlatform != "" && storedPlatform != "" && assistant.NormalizePlatformID(currentPlatform) != assistant.NormalizePlatformID(storedPlatform) {
		return false
	}
	return true
}

// ClaimNextInboundEvent atomically leases the highest-priority available event,
// preserving FIFO order within each priority. Expired processing leases are
// eligible for recovery by another worker.
func (s *SQLiteStore) ClaimNextInboundEvent(ctx context.Context, leaseOwner string, leaseUntil time.Time, groupConcurrency ...int) (assistant.InboundQueueItem, bool, error) {
	if s == nil || s.db == nil {
		return assistant.InboundQueueItem{}, false, errors.New("claim inbound event: sqlite store is not configured")
	}
	leaseOwner = strings.TrimSpace(leaseOwner)
	if leaseOwner == "" {
		return assistant.InboundQueueItem{}, false, errors.New("claim inbound event: lease owner is required")
	}
	now := time.Now().UTC()
	if leaseUntil.IsZero() || !leaseUntil.After(now) {
		return assistant.InboundQueueItem{}, false, errors.New("claim inbound event: lease must expire in the future")
	}
	groupLimit := inboundGroupConcurrencyValue(groupConcurrency)

	var item assistant.InboundQueueItem
	var payload string
	err := s.db.QueryRowContext(ctx, `
WITH candidate AS (
  SELECT queued.id
  FROM inbound_events AS queued
  WHERE ((queued.status = ? AND queued.available_at <= ?)
     OR (queued.status = ? AND queued.lease_until IS NOT NULL AND queued.lease_until <= ?))
    AND (
      SELECT COUNT(*)
      FROM inbound_events AS active
      WHERE active.session = queued.session
        AND active.status = ?
        AND active.lease_until > ?
    ) < CASE WHEN queued.kind = ? THEN ? ELSE 1 END
  ORDER BY
    queued.priority DESC,
    queued.event_time ASC,
    queued.created_at ASC,
    queued.id ASC
  LIMIT 1
)
UPDATE inbound_events
	SET status = ?,
	    lease_owner = ?,
	    lease_until = ?,
	    attempts = attempts + 1,
	    decision = NULL,
	    decision_reason = NULL,
	    reply_text = NULL,
	    processing_error = NULL,
	    duration_ms = NULL,
	    updated_at = ?
WHERE id = (SELECT id FROM candidate)
RETURNING id, session, payload, attempts, priority
`, inboundStatusPending, now.UnixNano(), inboundStatusProcessing, now.UnixNano(), inboundStatusProcessing, now.UnixNano(), string(assistant.EventKindGroup), groupLimit,
		inboundStatusProcessing, leaseOwner, leaseUntil.UTC().UnixNano(), now.UnixNano()).Scan(
		&item.ID, &item.Session, &payload, &item.Attempts, &item.Priority,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return assistant.InboundQueueItem{}, false, nil
		}
		return assistant.InboundQueueItem{}, false, fmt.Errorf("claim inbound event: %w", err)
	}
	if err := json.Unmarshal([]byte(payload), &item.Event); err != nil {
		return assistant.InboundQueueItem{}, false, fmt.Errorf("decode inbound event %q: %w", item.ID, err)
	}
	return item, true, nil
}

func inboundPriorityValue(values []int) int {
	if len(values) == 0 || values[0] < 0 {
		return assistant.InboundPriorityNormal
	}
	return values[0]
}

func inboundGroupConcurrencyValue(values []int) int {
	if len(values) == 0 || values[0] <= 0 {
		return 1
	}
	return values[0]
}

// CompleteInboundEvent marks a leased event terminal without deleting its
// audit record.
func (s *SQLiteStore) CompleteInboundEvent(ctx context.Context, id string, leaseOwner string, outcome string) error {
	if s == nil || s.db == nil {
		return errors.New("complete inbound event: sqlite store is not configured")
	}
	id = strings.TrimSpace(id)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if id == "" || leaseOwner == "" {
		return errors.New("complete inbound event: id and lease owner are required")
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET status = ?, outcome = ?, last_error = NULL,
    lease_owner = NULL, lease_until = NULL,
    completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND lease_owner = ?
`, inboundStatusDone, strings.TrimSpace(outcome), now, now, id, inboundStatusProcessing, leaseOwner)
	if err != nil {
		return fmt.Errorf("complete inbound event %q: %w", id, err)
	}
	return requireInboundLeaseUpdate(result, "complete", id)
}

// RetryInboundEvent returns a leased event to the queue at the requested time.
func (s *SQLiteStore) RetryInboundEvent(ctx context.Context, id string, leaseOwner string, availableAt time.Time, lastError string) error {
	if s == nil || s.db == nil {
		return errors.New("retry inbound event: sqlite store is not configured")
	}
	id = strings.TrimSpace(id)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if id == "" || leaseOwner == "" {
		return errors.New("retry inbound event: id and lease owner are required")
	}
	now := time.Now().UTC()
	if availableAt.IsZero() {
		availableAt = now
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
	SET status = ?, available_at = ?, last_error = ?, outcome = NULL,
	    decision = NULL, decision_reason = NULL, reply_text = NULL,
	    processing_error = NULL, duration_ms = NULL, delivery_stage = NULL,
	    outbound_message_id = NULL, reply_generated_at = NULL, send_attempted_at = NULL,
	    send_acked_at = NULL, self_echo_at = NULL, delivery_error = NULL,
	    lease_owner = NULL, lease_until = NULL, completed_at = NULL, updated_at = ?
WHERE id = ? AND status = ? AND lease_owner = ?
`, inboundStatusPending, availableAt.UTC().UnixNano(), strings.TrimSpace(lastError), now.UnixNano(), id, inboundStatusProcessing, leaseOwner)
	if err != nil {
		return fmt.Errorf("retry inbound event %q: %w", id, err)
	}
	return requireInboundLeaseUpdate(result, "retry", id)
}

// ReleaseInboundLeases immediately returns every lease held by one worker to
// the pending queue, for example during a graceful shutdown.
func (s *SQLiteStore) ReleaseInboundLeases(ctx context.Context, leaseOwner string) error {
	if s == nil || s.db == nil {
		return errors.New("release inbound leases: sqlite store is not configured")
	}
	leaseOwner = strings.TrimSpace(leaseOwner)
	now := time.Now().UTC().UnixNano()
	query := `
UPDATE inbound_events
SET status = ?, available_at = ?, lease_owner = NULL, lease_until = NULL, updated_at = ?
WHERE status = ?`
	args := []any{inboundStatusPending, now, now, inboundStatusProcessing}
	if leaseOwner != "" {
		query += ` AND lease_owner = ?`
		args = append(args, leaseOwner)
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("release inbound leases for %q: %w", leaseOwner, err)
	}
	return nil
}

// PendingInboundCount reports all non-terminal work, including currently
// leased events.
func (s *SQLiteStore) PendingInboundCount(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("count pending inbound events: sqlite store is not configured")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbound_events WHERE status IN (?, ?)
`, inboundStatusPending, inboundStatusProcessing).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending inbound events: %w", err)
	}
	return count, nil
}

// GroupHistoryWatermark returns the newest persisted event timestamp for one
// group, including history that predates the durable queue migration.
func (s *SQLiteStore) GroupHistoryWatermark(ctx context.Context, groupID string) (int64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, errors.New("load group history watermark: sqlite store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return 0, false, errors.New("load group history watermark: group id is required")
	}
	var watermark sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
SELECT MAX(event_time)
FROM message_events
WHERE kind = ? AND group_id = ?
`, string(assistant.EventKindGroup), groupID).Scan(&watermark); err != nil {
		return 0, false, fmt.Errorf("load group history watermark %q: %w", groupID, err)
	}
	if !watermark.Valid {
		return 0, false, nil
	}
	return watermark.Int64, true, nil
}

// ListHistorySessions returns each known group/private conversation and its
// latest persisted event time for reconnect backfill.
func (s *SQLiteStore) ListHistorySessions(ctx context.Context) ([]assistant.HistorySession, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("list history sessions: sqlite store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT kind, session_id, MAX(event_time)
FROM (
  SELECT kind, group_id AS session_id, event_time
  FROM message_events
  WHERE kind = ? AND group_id IS NOT NULL AND group_id != ''
  UNION ALL
  SELECT kind, user_id AS session_id, event_time
  FROM message_events
  WHERE kind = ? AND user_id IS NOT NULL AND user_id != ''
)
GROUP BY kind, session_id
ORDER BY kind ASC, session_id ASC
`, string(assistant.EventKindGroup), string(assistant.EventKindPrivate))
	if err != nil {
		return nil, fmt.Errorf("list history sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]assistant.HistorySession, 0)
	for rows.Next() {
		var kind string
		var item assistant.HistorySession
		if err := rows.Scan(&kind, &item.ID, &item.LastEventTime); err != nil {
			return nil, fmt.Errorf("scan history session: %w", err)
		}
		item.Kind = assistant.EventKind(kind)
		sessions = append(sessions, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list history sessions: %w", err)
	}
	return sessions, nil
}

func stableInboundEventID(session string, event assistant.MessageEvent) (string, error) {
	if strings.TrimSpace(event.MessageID) != "" {
		return persistedMessageID(session, event), nil
	}
	canonical, err := json.Marshal(struct {
		Session     string                     `json:"session"`
		Kind        assistant.EventKind        `json:"kind"`
		SubType     string                     `json:"sub_type,omitempty"`
		Time        int64                      `json:"time,omitempty"`
		SelfID      string                     `json:"self_id,omitempty"`
		UserID      string                     `json:"user_id,omitempty"`
		OperatorID  string                     `json:"operator_id,omitempty"`
		GroupID     string                     `json:"group_id,omitempty"`
		MessageType string                     `json:"message_type,omitempty"`
		RawMessage  string                     `json:"raw_message,omitempty"`
		Segments    []assistant.MessageSegment `json:"segments,omitempty"`
	}{
		Session:     session,
		Kind:        event.Kind,
		SubType:     event.SubType,
		Time:        event.Time,
		SelfID:      event.SelfID,
		UserID:      event.UserID,
		OperatorID:  event.OperatorID,
		GroupID:     event.GroupID,
		MessageType: event.MessageType,
		RawMessage:  event.RawMessage,
		Segments:    event.Segments,
	})
	if err != nil {
		return "", fmt.Errorf("encode inbound event identity: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func requireInboundLeaseUpdate(result sql.Result, action string, id string) error {
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s inbound event %q: inspect update: %w", action, id, err)
	}
	if updated == 0 {
		return fmt.Errorf("%s inbound event %q: lease is not held by this worker", action, id)
	}
	return nil
}
