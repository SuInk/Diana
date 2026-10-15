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

var _ assistant.InboundMessageEditStore = (*SQLiteStore)(nil)

// EnqueueInboundEvent atomically records message history and makes a new event
// available to the durable worker queue. Existing history rows remain
// deduplicated so pre-queue chat history is never replayed after an upgrade.
func (s *SQLiteStore) EnqueueInboundEvent(ctx context.Context, session string, event assistant.MessageEvent, priorities ...int) (string, bool, error) {
	defer s.observeStorage(ctx, "EnqueueInboundEvent", "write")()
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

	tx, err := s.beginWriteTx(ctx, "EnqueueInboundEvent")
	if err != nil {
		return "", false, fmt.Errorf("begin inbound enqueue: %w", err)
	}
	defer observeTransaction("EnqueueInboundEvent")()
	defer func() { _ = tx.Rollback() }()

	if duplicate, found, findErr := findDuplicateInboundHistory(ctx, tx, event); findErr != nil {
		return "", false, findErr
	} else if found {
		editApplied := false
		currentEvent := event
		if event.Edited && editedInboundSupersedes(event, duplicate.Event) {
			currentEvent, payload, text, err = prepareEditedInboundRevision(event, duplicate.Event)
			if err != nil {
				return "", false, err
			}
			editApplied, err = s.refreshEditedInboundTx(ctx, tx, duplicate, currentEvent, string(payload), text, priority, now)
			if err != nil {
				return "", false, err
			}
		} else if _, err := tx.ExecContext(ctx, `
UPDATE inbound_events
SET priority = CASE WHEN priority < ? THEN ? ELSE priority END,
    updated_at = ?
WHERE id = ? AND status = ?
			`, priority, priority, now.UnixNano(), duplicate.ID, inboundStatusPending); err != nil {
			return "", false, fmt.Errorf("refresh duplicate inbound priority: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return "", false, fmt.Errorf("commit duplicate inbound event: %w", err)
		}
		if editApplied {
			s.refreshEditedMessageIndexes(ctx, duplicate.ID, duplicate.Session, currentEvent)
		}
		return duplicate.ID, false, nil
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
	// 只带图、没有文字的消息以前要先压 15 秒，等同一个人紧跟的那句话来把它并走。
	// 合并已经去掉，所有消息进来就能处理。
	availableAt := now
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
	defer s.observeStorage(ctx, "RecordNoticeEvent", "write")()
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
	outcome := "notice_" + strings.TrimSpace(event.SubType)
	reason := "已记录平台通知"
	if strings.EqualFold(strings.TrimSpace(event.OperatorRole), "telegram_recall_probe") {
		outcome = "telegram_message_unavailable"
		reason = "Telegram 按需探测发现消息当前已不可访问"
	}
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
		inboundStatusDone, now, outcome, "notice", reason, now, now, now)
	if err != nil {
		return fmt.Errorf("record notice event: %w", err)
	}
	return nil
}

type duplicateInboundHistory struct {
	ID      string
	Session string
	Event   assistant.MessageEvent
}

func findDuplicateInboundHistory(ctx context.Context, tx *sql.Tx, event assistant.MessageEvent) (duplicateInboundHistory, bool, error) {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" {
		return duplicateInboundHistory{}, false, nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, session, payload
FROM message_events
WHERE message_id = ?
  AND kind = ?
  AND COALESCE(group_id, '') = ?
  AND COALESCE(user_id, '') = ?
ORDER BY event_time DESC, created_at DESC, id DESC
`, messageID, string(event.Kind), strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID))
	if err != nil {
		return duplicateInboundHistory{}, false, fmt.Errorf("find duplicate inbound history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var duplicate duplicateInboundHistory
		var payload string
		if err := rows.Scan(&duplicate.ID, &duplicate.Session, &payload); err != nil {
			return duplicateInboundHistory{}, false, fmt.Errorf("scan duplicate inbound history: %w", err)
		}
		if json.Unmarshal([]byte(payload), &duplicate.Event) != nil || !sameInboundTransport(event, duplicate.Event) {
			continue
		}
		return duplicate, true, nil
	}
	if err := rows.Err(); err != nil {
		return duplicateInboundHistory{}, false, fmt.Errorf("iterate duplicate inbound history: %w", err)
	}
	return duplicateInboundHistory{}, false, nil
}

// RefreshInboundMessageEdit updates an already known message revision without
// creating another reply turn. A missing original is reported as Found=false so
// the caller can feed the edit through the ordinary first-seen message path.
func (s *SQLiteStore) RefreshInboundMessageEdit(ctx context.Context, session string, event assistant.MessageEvent, priorities ...int) (assistant.InboundMessageEditRefresh, error) {
	defer s.observeStorage(ctx, "RefreshInboundMessageEdit", "write")()
	if s == nil || s.db == nil {
		return assistant.InboundMessageEditRefresh{}, errors.New("refresh inbound message edit: sqlite store is not configured")
	}
	session = strings.TrimSpace(session)
	if session == "" || !event.Edited || strings.TrimSpace(event.MessageID) == "" {
		return assistant.InboundMessageEditRefresh{}, nil
	}
	now := time.Now().UTC()
	if event.Time <= 0 {
		event.Time = now.Unix()
	}
	var err error
	event, err = s.persistInboundVoiceBlobs(ctx, event)
	if err != nil {
		return assistant.InboundMessageEditRefresh{}, err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return assistant.InboundMessageEditRefresh{}, fmt.Errorf("encode inbound message edit: %w", err)
	}
	text := strings.TrimSpace(assistant.PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	tx, err := s.beginWriteTx(ctx, "RefreshInboundMessageEdit")
	if err != nil {
		return assistant.InboundMessageEditRefresh{}, fmt.Errorf("begin inbound message edit: %w", err)
	}
	defer observeTransaction("RefreshInboundMessageEdit")()
	defer func() { _ = tx.Rollback() }()
	duplicate, found, err := findDuplicateInboundHistory(ctx, tx, event)
	if err != nil || !found {
		return assistant.InboundMessageEditRefresh{}, err
	}
	result := assistant.InboundMessageEditRefresh{EventID: duplicate.ID, Session: duplicate.Session, Event: duplicate.Event, Found: true}
	applied := false
	if editedInboundSupersedes(event, duplicate.Event) {
		event, payload, text, err = prepareEditedInboundRevision(event, duplicate.Event)
		if err != nil {
			return result, err
		}
		applied, err = s.refreshEditedInboundTx(ctx, tx, duplicate, event, string(payload), text, inboundPriorityValue(priorities), now)
		if err != nil {
			return result, err
		}
		result.Event = event
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit inbound message edit: %w", err)
	}
	if applied {
		s.refreshEditedMessageIndexes(ctx, duplicate.ID, duplicate.Session, event)
	}
	result.Applied = applied
	return result, nil
}

func prepareEditedInboundRevision(event, stored assistant.MessageEvent) (assistant.MessageEvent, []byte, string, error) {
	event.EditRefreshOnly = true
	event = mergeEditedMediaDerivedFields(event, stored)
	payload, err := json.Marshal(event)
	if err != nil {
		return assistant.MessageEvent{}, nil, "", fmt.Errorf("encode inbound message edit: %w", err)
	}
	text := strings.TrimSpace(assistant.PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	return event, payload, text, nil
}

var editedMediaDerivedKeys = []string{
	"cached_file",
	"cached_mime",
	"content_sha256",
	"cached_description",
	"cached_description_source",
	"cached_description_failure",
	"transcript",
	"audio_sha256",
	"cached_blob_sha256",
	"stt_backend",
	"stt_model",
	"stt_error_code",
}

// mergeEditedMediaDerivedFields keeps work already derived from an unchanged
// attachment. Telegram edit updates carry fresh transport metadata but do not
// contain Diana's cached path, image hash, visual description, or transcript.
// Replacing the attachment changes its stable identity and therefore copies
// nothing from the previous revision.
func mergeEditedMediaDerivedFields(event, stored assistant.MessageEvent) assistant.MessageEvent {
	event.Segments = mergeEditedMediaSegments(event.Segments, stored.Segments)
	if event.Quoted != nil && stored.Quoted != nil {
		quoted := *event.Quoted
		quoted.Segments = mergeEditedMediaSegments(quoted.Segments, stored.Quoted.Segments)
		event.Quoted = &quoted
	}
	return event
}

func mergeEditedMediaSegments(incoming, stored []assistant.MessageSegment) []assistant.MessageSegment {
	incoming = append([]assistant.MessageSegment(nil), incoming...)
	used := make([]bool, len(stored))
	for incomingIndex := range incoming {
		identity := editedMediaIdentity(incoming[incomingIndex])
		if identity == "" {
			continue
		}
		for storedIndex := range stored {
			if used[storedIndex] || stored[storedIndex].Type != incoming[incomingIndex].Type || editedMediaIdentity(stored[storedIndex]) != identity {
				continue
			}
			used[storedIndex] = true
			data := make(map[string]string, len(incoming[incomingIndex].Data)+len(editedMediaDerivedKeys))
			for key, value := range incoming[incomingIndex].Data {
				data[key] = value
			}
			for _, key := range editedMediaDerivedKeys {
				if strings.TrimSpace(data[key]) == "" && strings.TrimSpace(stored[storedIndex].Data[key]) != "" {
					data[key] = stored[storedIndex].Data[key]
				}
			}
			incoming[incomingIndex].Data = data
			break
		}
	}
	return incoming
}

func editedMediaIdentity(segment assistant.MessageSegment) string {
	switch segment.Type {
	case "image", "video", "record", "file":
	default:
		return ""
	}
	for _, key := range []string{
		"sticker_unique_id", "file_unique_id",
		"content_sha256", "audio_sha256", "cached_blob_sha256",
		"sticker_file_id", "file_id",
	} {
		if value := strings.TrimSpace(segment.Data[key]); value != "" {
			return key + ":" + value
		}
	}
	return ""
}

func editedInboundSupersedes(incoming, stored assistant.MessageEvent) bool {
	if !incoming.Edited {
		return false
	}
	if !stored.Edited {
		return true
	}
	// A repeated update ID is the same transport update, even if it is delivered
	// again after a commit-before-ack crash.
	if incoming.TelegramUpdateID > 0 && stored.TelegramUpdateID > 0 {
		if incoming.TelegramUpdateID == stored.TelegramUpdateID {
			return false
		}
	}
	// edit_date is the primary revision clock. update_id normally increases, but
	// Telegram may randomize it after a week without updates.
	if incoming.EditTime > 0 && stored.EditTime > 0 && incoming.EditTime != stored.EditTime {
		return incoming.EditTime > stored.EditTime
	}
	if incoming.EditTime > 0 && stored.EditTime == 0 {
		return true
	}
	if incoming.EditTime == 0 && stored.EditTime > 0 {
		return false
	}
	if incoming.TelegramUpdateID > 0 && stored.TelegramUpdateID > 0 {
		return incoming.TelegramUpdateID > stored.TelegramUpdateID
	}
	// Once a durable revision has a Telegram update ID, an older snapshot from
	// before this field existed cannot establish an order against it.
	if stored.TelegramUpdateID > 0 {
		return false
	}
	// Compatible gateways can omit both ordering fields. In that case arrival
	// order is the only revision order available.
	return true
}

func (s *SQLiteStore) refreshEditedInboundTx(ctx context.Context, tx *sql.Tx, duplicate duplicateInboundHistory, event assistant.MessageEvent, payload, text string, priority int, now time.Time) (bool, error) {
	mediaChanged := assistant.MessageSearchMediaRevision(event) != assistant.MessageSearchMediaRevision(duplicate.Event)
	if _, err := tx.ExecContext(ctx, `
UPDATE message_events
SET kind = ?, profile_id = ?, group_id = ?, user_id = ?, message_id = ?, sender_name = ?,
    text = ?, payload = ?, search_extra = CASE WHEN ? THEN NULL ELSE search_extra END
WHERE id = ?
`, string(event.Kind), strings.TrimSpace(event.ProfileID), event.GroupID, event.UserID, event.MessageID,
		event.SenderName, text, payload, mediaChanged, duplicate.ID); err != nil {
		return false, fmt.Errorf("refresh edited message history: %w", err)
	}
	// Pending work has not taken a processing snapshot yet, so let it consume the
	// current revision. Processing/done rows keep the payload that produced their
	// audit result and are never reopened by an edit.
	if _, err := tx.ExecContext(ctx, `
UPDATE inbound_events
SET profile_id = CASE WHEN TRIM(COALESCE(profile_id, '')) = '' THEN ? ELSE profile_id END,
    priority = CASE WHEN status = ? AND priority < ? THEN ? ELSE priority END,
    payload = CASE WHEN status = ? THEN ? ELSE payload END,
    updated_at = CASE WHEN status = ? THEN ? ELSE updated_at END
WHERE id = ?
`, strings.TrimSpace(event.ProfileID), inboundStatusPending, priority, priority, inboundStatusPending, payload,
		inboundStatusPending, now.UnixNano(), duplicate.ID); err != nil {
		return false, fmt.Errorf("refresh edited inbound payload: %w", err)
	}
	var extra sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT search_extra FROM message_events WHERE id = ?`, duplicate.ID).Scan(&extra); err != nil {
		return false, fmt.Errorf("load edited message search extra: %w", err)
	}
	if err := s.indexMessageHistoryRowTx(tx, duplicate.ID, event.SenderName, event.UserID, text, strings.TrimSpace(extra.String)); err != nil {
		return false, fmt.Errorf("index edited message history: %w", err)
	}
	return true, nil
}

func (s *SQLiteStore) refreshEditedMessageIndexes(ctx context.Context, id, session string, event assistant.MessageEvent) {
	if s.historyVectors {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM `+messageHistoryVectorTable+` WHERE id = ?`, id)
	}
	if err := s.indexStickerAssets(ctx, session, event); err != nil {
		// The message row is already committed. Sticker indexing is a derived cache
		// and must not turn a successful edit into a transport retry.
		return
	}
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
func (s *SQLiteStore) ClaimNextInboundEvent(ctx context.Context, leaseOwner string, leaseUntil time.Time, limits ...assistant.InboundConcurrency) (assistant.InboundQueueItem, bool, error) {
	defer s.observeStorage(ctx, "ClaimNextInboundEvent", "write")()
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
	concurrency := inboundConcurrencyValue(limits)

	var item assistant.InboundQueueItem
	var payload string
	var enqueuedAt int64
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
    ) < CASE WHEN queued.kind = ? THEN ? ELSE ? END
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
RETURNING id, session, payload, attempts, priority, created_at
`, inboundStatusPending, now.UnixNano(), inboundStatusProcessing, now.UnixNano(), inboundStatusProcessing, now.UnixNano(),
		string(assistant.EventKindGroup), concurrency.Group, concurrency.Private,
		inboundStatusProcessing, leaseOwner, leaseUntil.UTC().UnixNano(), now.UnixNano()).Scan(
		&item.ID, &item.Session, &payload, &item.Attempts, &item.Priority, &enqueuedAt,
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
	if enqueuedAt > 0 {
		item.EnqueuedAt = time.Unix(0, enqueuedAt)
	}
	return item, true, nil
}

// InboundSessionHasNewerPending 看同一会话里在这条之后还有没有等着处理的消息。
func (s *SQLiteStore) InboundSessionHasNewerPending(ctx context.Context, item assistant.InboundQueueItem) (bool, error) {
	defer s.observeStorage(ctx, "InboundSessionHasNewerPending", "read")()
	if s == nil || s.db == nil {
		return false, errors.New("check newer pending inbound event: sqlite store is not configured")
	}
	if strings.TrimSpace(item.Session) == "" {
		return false, nil
	}
	var exists int
	// 只读判断，走读池：写连接上排队的入队、领取不该让它跟着等。还在提交中的
	// 入队这次看不到，等同于它晚到了一点，下一条照常会被合并判断到。
	err := s.eventReader().QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM inbound_events
  WHERE session = ? AND status = ? AND id != ?
    AND (event_time > ? OR (event_time = ? AND created_at > ?))
)
`, item.Session, inboundStatusPending, item.ID, item.Event.Time, item.Event.Time, item.EnqueuedAt.UnixNano()).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check newer pending inbound event: %w", err)
	}
	return exists == 1, nil
}

func inboundPriorityValue(values []int) int {
	if len(values) == 0 || values[0] < 0 {
		return assistant.InboundPriorityNormal
	}
	return values[0]
}

// inboundConcurrencyValue 归一化调用方给的并发上限。没给或给了非正数时退回 1：
// 认领查询按会话串行是最保守的行为，绝不能因为配置缺失变成无上限。
func inboundConcurrencyValue(values []assistant.InboundConcurrency) assistant.InboundConcurrency {
	limits := assistant.InboundConcurrency{}
	if len(values) > 0 {
		limits = values[0]
	}
	if limits.Group <= 0 {
		limits.Group = 1
	}
	if limits.Private <= 0 {
		limits.Private = 1
	}
	return limits
}

// CompleteInboundEvent marks a leased event terminal without deleting its
// audit record.
func (s *SQLiteStore) CompleteInboundEvent(ctx context.Context, id string, leaseOwner string, outcome string) error {
	defer s.observeStorage(ctx, "CompleteInboundEvent", "write")()
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
	defer s.observeStorage(ctx, "RetryInboundEvent", "write")()
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

// ExtendInboundLease pushes a held lease out to at least leaseUntil. It never
// shortens a lease and only succeeds for the worker that still holds it.
func (s *SQLiteStore) ExtendInboundLease(ctx context.Context, id string, leaseOwner string, leaseUntil time.Time) error {
	defer s.observeStorage(ctx, "ExtendInboundLease", "write")()
	if s == nil || s.db == nil {
		return errors.New("extend inbound lease: sqlite store is not configured")
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET lease_until = MAX(COALESCE(lease_until, 0), ?), updated_at = ?
WHERE id = ? AND status = ? AND lease_owner = ?`,
		leaseUntil.UTC().UnixNano(), now, strings.TrimSpace(id), inboundStatusProcessing, strings.TrimSpace(leaseOwner))
	if err != nil {
		return fmt.Errorf("extend inbound event %q: %w", id, err)
	}
	return requireInboundLeaseUpdate(result, "extend", id)
}

// ReleaseInboundLeases immediately returns every lease held by one worker to
// the pending queue, for example during a graceful shutdown.
func (s *SQLiteStore) ReleaseInboundLeases(ctx context.Context, leaseOwner string) error {
	defer s.observeStorage(ctx, "ReleaseInboundLeases", "write")()
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
	defer s.observeStorage(ctx, "PendingInboundCount", "read")()
	if s == nil || s.db == nil {
		return 0, errors.New("count pending inbound events: sqlite store is not configured")
	}
	// 状态展示用的计数，调用方只给 250ms。放在写连接上时，一有写入排队它就
	// 超时，生产日志里大量「PendingInboundCount 被取消」都是这么来的。
	var count int
	if err := s.eventReader().QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbound_events WHERE status IN (?, ?)
`, inboundStatusPending, inboundStatusProcessing).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending inbound events: %w", err)
	}
	return count, nil
}

// GroupHistoryWatermark returns the newest persisted event timestamp for one
// group, including history that predates the durable queue migration.
func (s *SQLiteStore) GroupHistoryWatermark(ctx context.Context, groupID string) (int64, bool, error) {
	defer s.observeStorage(ctx, "GroupHistoryWatermark", "read")()
	if s == nil || s.db == nil {
		return 0, false, errors.New("load group history watermark: sqlite store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return 0, false, errors.New("load group history watermark: group id is required")
	}
	// 回补水位只决定从哪里开始拉历史，读到的若比正在提交的那条早一点，只是多
	// 拉几条，入库去重会挡掉，所以不必排在写连接后面。
	var watermark sql.NullInt64
	if err := s.eventReader().QueryRowContext(ctx, `
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

// historySessionsQuery 的平台、机器人表达式必须和 historySessionsIndex 里的逐字一致。
// payload 不是合法 JSON 时平台、机器人按空值算（原先整条查询会报错）。
const historySessionsQuery = `
SELECT kind, session_id, platform, profile_id, MAX(event_time)
FROM (
  SELECT kind, group_id AS session_id,
         COALESCE(CASE WHEN json_valid(payload) THEN json_extract(payload, '$.platform') END, '') AS platform,
         COALESCE(profile_id, CASE WHEN json_valid(payload) THEN json_extract(payload, '$.profile_id') END, '') AS profile_id,
         event_time
  FROM message_events
  WHERE kind = ? AND group_id IS NOT NULL AND group_id != ''
  UNION ALL
  SELECT kind, user_id AS session_id,
         COALESCE(CASE WHEN json_valid(payload) THEN json_extract(payload, '$.platform') END, '') AS platform,
         COALESCE(profile_id, CASE WHEN json_valid(payload) THEN json_extract(payload, '$.profile_id') END, '') AS profile_id,
         event_time
  FROM message_events
  WHERE kind = ? AND user_id IS NOT NULL AND user_id != ''
)
GROUP BY kind, session_id, platform, profile_id
ORDER BY platform ASC, profile_id ASC, kind ASC, session_id ASC
`

// ListHistorySessions returns each known group/private conversation and its
// latest persisted event time for reconnect backfill.
func (s *SQLiteStore) ListHistorySessions(ctx context.Context) ([]assistant.HistorySession, error) {
	defer s.observeStorage(ctx, "ListHistorySessions", "read")()
	if s == nil || s.db == nil {
		return nil, errors.New("list history sessions: sqlite store is not configured")
	}
	// 要扫全部消息历史，大库上接近一秒。原先占着唯一的写连接，重连回补期间
	// 入队和领取全部排在它后面；改到读池，并由 idx_message_events_history_sessions
	// 覆盖，不再回表解析 payload。它只是回补的起点快照，读池看到的已提交数据足够。
	rows, err := s.eventReader().QueryContext(ctx, historySessionsQuery, string(assistant.EventKindGroup), string(assistant.EventKindPrivate))
	if err != nil {
		return nil, fmt.Errorf("list history sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]assistant.HistorySession, 0)
	for rows.Next() {
		var kind string
		var item assistant.HistorySession
		if err := rows.Scan(&kind, &item.ID, &item.Platform, &item.ProfileID, &item.LastEventTime); err != nil {
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

// GroupHistoryAnchors 返回这个群最近 limit 条带 seq 的消息，新的在前，给群回补当往新
// 方向翻的锚点。机器人自己发的消息本地不带 seq，自然不在里面。
func (s *SQLiteStore) GroupHistoryAnchors(ctx context.Context, profileID, groupID string, limit int) ([]assistant.HistoryAnchor, error) {
	defer s.observeStorage(ctx, "GroupHistoryAnchors", "read")()
	if s == nil || s.db == nil {
		return nil, errors.New("group history anchors: sqlite store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" || limit <= 0 {
		return nil, nil
	}
	profileID = strings.TrimSpace(profileID)
	// 走 idx_message_events_kind_group_time 按时间倒着扫，凑够 limit 条就停。
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT message_id, seq
FROM (
  SELECT COALESCE(NULLIF(message_id, ''), CASE WHEN json_valid(payload) THEN json_extract(payload, '$.message_id') END) AS message_id,
         CASE WHEN json_valid(payload) THEN CAST(json_extract(payload, '$.message_seq') AS INTEGER) END AS seq,
         event_time
  FROM message_events
  WHERE kind = ? AND group_id = ? AND id NOT LIKE '%:notice:%'
    AND (? = '' OR COALESCE(profile_id, CASE WHEN json_valid(payload) THEN json_extract(payload, '$.profile_id') END, '') IN (?, ''))
  ORDER BY event_time DESC
)
WHERE seq > 0 AND COALESCE(message_id, '') != ''
LIMIT ?
`, string(assistant.EventKindGroup), groupID, profileID, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("group history anchors %q: %w", groupID, err)
	}
	defer func() { _ = rows.Close() }()
	anchors := make([]assistant.HistoryAnchor, 0, limit)
	for rows.Next() {
		var anchor assistant.HistoryAnchor
		if err := rows.Scan(&anchor.MessageID, &anchor.Seq); err != nil {
			return nil, fmt.Errorf("scan group history anchor: %w", err)
		}
		anchors = append(anchors, anchor)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("group history anchors %q: %w", groupID, err)
	}
	return anchors, nil
}

// GroupSeqGap 比较重连后收到的实时群消息和库里这个群上一条带 seq 的消息，算出中间缺了
// 几条。机器人自己发的消息本地不带 seq，但同样占号，按条数扣掉。
func (s *SQLiteStore) GroupSeqGap(ctx context.Context, query assistant.GroupSeqGapQuery) (assistant.GroupSeqGap, error) {
	defer s.observeStorage(ctx, "GroupSeqGap", "read")()
	if s == nil || s.db == nil {
		return assistant.GroupSeqGap{}, errors.New("group seq gap: sqlite store is not configured")
	}
	groupID := strings.TrimSpace(query.GroupID)
	if groupID == "" || query.Seq <= 1 {
		return assistant.GroupSeqGap{}, nil
	}
	profileID := strings.TrimSpace(query.ProfileID)
	// 比较的都是这条实时消息之前、早已提交的记录，读池就够，不用排在写入后面。
	var previousSeq, previousTime int64
	err := s.eventReader().QueryRowContext(ctx, `
SELECT seq, event_time
FROM (
  SELECT CAST(json_extract(payload, '$.message_seq') AS INTEGER) AS seq, event_time
  FROM message_events
  WHERE kind = ? AND group_id = ? AND event_time >= ? AND event_time <= ?
    AND (? = '' OR COALESCE(profile_id, json_extract(payload, '$.profile_id'), '') IN (?, ''))
)
WHERE seq > 0 AND seq < ?
ORDER BY seq DESC
LIMIT 1
`, string(assistant.EventKindGroup), groupID, query.Since, query.EventTime, profileID, profileID, query.Seq).Scan(&previousSeq, &previousTime)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.GroupSeqGap{}, nil
	}
	if err != nil {
		return assistant.GroupSeqGap{}, fmt.Errorf("group seq gap %q: %w", groupID, err)
	}
	gap := assistant.GroupSeqGap{Known: true, PreviousSeq: previousSeq, PreviousTime: previousTime}
	if selfID := strings.TrimSpace(query.SelfID); selfID != "" {
		if err := s.eventReader().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM message_events
WHERE kind = ? AND group_id = ? AND user_id = ? AND event_time >= ? AND event_time <= ?
`, string(assistant.EventKindGroup), groupID, selfID, previousTime, query.EventTime).Scan(&gap.SelfMessages); err != nil {
			return assistant.GroupSeqGap{}, fmt.Errorf("group seq gap %q: count self messages: %w", groupID, err)
		}
	}
	gap.Missing = int(query.Seq-previousSeq-1) - gap.SelfMessages
	if gap.Missing < 0 {
		gap.Missing = 0
	}
	return gap, nil
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
