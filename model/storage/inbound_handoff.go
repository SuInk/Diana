// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 连发交接的持久化（见 model/assistant/inbound_handoff.go）。交接状态落在两列上：
// superseded_by 是接手那一轮的入站事件 ID，handoff_state 是 pending / final。
// 被交出去的那一轮以 handed_off_pending 收尾；撤销交接时，已经收尾的那行直接回到
// 队列、提高优先级，让它自己回答。
var _ assistant.InboundHandoffStore = (*SQLiteStore)(nil)

const (
	inboundHandoffPending = "pending"
	inboundHandoffFinal   = "final"
	// assistantOutcomeHandedOffPending 和 assistant 包里的 inboundOutcomeHandedOffPending 同值。
	assistantOutcomeHandedOffPending = "handed_off_pending"
)

// addInboundEventHandoffColumns 给事件表补上交接状态列。
func (s *SQLiteStore) addInboundEventHandoffColumns() error {
	for _, column := range []struct{ name, ddl string }{
		{"handoff_state", `ALTER TABLE inbound_events ADD COLUMN handoff_state TEXT`},
		{"handoff_at", `ALTER TABLE inbound_events ADD COLUMN handoff_at INTEGER`},
	} {
		has, err := s.hasColumn("inbound_events", column.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.Exec(column.ddl); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_inbound_events_handoff ON inbound_events(handoff_state) WHERE handoff_state IS NOT NULL`)
	return err
}

// inboundHandoffRow 给出定位那一行的 WHERE 条件。有队列事件 ID 就走主键；没有才按
// 会话、发送者和消息 ID 找最新那行——那条查询没有索引，唯一的写连接上一次要扫几十毫秒。
func inboundHandoffRow(ref assistant.InboundHandoffRef) (string, []any) {
	if id := strings.TrimSpace(ref.ID); id != "" {
		return `id = ?`, []any{id}
	}
	event := ref.Event
	return `id = (
  SELECT id FROM inbound_events
  WHERE message_id = ? AND kind = ?
    AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
  ORDER BY created_at DESC, id DESC LIMIT 1
)`, []any{strings.TrimSpace(event.MessageID), strings.TrimSpace(string(event.Kind)), strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID)}
}

func inboundHandoffRefUsable(ref assistant.InboundHandoffRef) bool {
	return strings.TrimSpace(ref.ID) != "" || strings.TrimSpace(ref.Event.MessageID) != ""
}

func (s *SQLiteStore) MarkInboundHandoff(ctx context.Context, ref assistant.InboundHandoffRef, absorberID string) error {
	defer s.observeStorage(ctx, "MarkInboundHandoff", "write")()
	if s == nil || s.db == nil || !inboundHandoffRefUsable(ref) || strings.TrimSpace(absorberID) == "" {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	where, whereArgs := inboundHandoffRow(ref)
	args := append([]any{strings.TrimSpace(absorberID), inboundHandoffPending, now, now}, whereArgs...)
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events SET superseded_by = ?, handoff_state = ?, handoff_at = ?, updated_at = ?
WHERE `+where+`
  AND COALESCE(superseded_by, '') = ''
`, args...)
	if err != nil {
		return fmt.Errorf("mark inbound handoff: %w", err)
	}
	return nil
}

func (s *SQLiteStore) FinalizeInboundHandoff(ctx context.Context, ref assistant.InboundHandoffRef, absorberID string) error {
	defer s.observeStorage(ctx, "FinalizeInboundHandoff", "write")()
	if s == nil || s.db == nil || !inboundHandoffRefUsable(ref) || strings.TrimSpace(absorberID) == "" {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	where, whereArgs := inboundHandoffRow(ref)
	args := append([]any{inboundHandoffFinal, inboundStatusDone, assistantOutcomeHandedOffPending, now}, whereArgs...)
	args = append(args, strings.TrimSpace(absorberID), inboundHandoffPending)
	_, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET handoff_state = ?,
    outcome = CASE WHEN status = ? AND outcome = ? THEN 'superseded_follow_up' ELSE outcome END,
    updated_at = ?
WHERE `+where+`
  AND superseded_by = ? AND handoff_state = ?
`, args...)
	if err != nil {
		return fmt.Errorf("finalize inbound handoff: %w", err)
	}
	return nil
}

// ReleaseInboundHandoff 撤销交接。那一行要是已经以 handed_off_pending 收尾，就回到队列：
// 立即可领、优先级提到直接触发那一档。还在处理中的只清交接列，它自己会接着回答。
func (s *SQLiteStore) ReleaseInboundHandoff(ctx context.Context, ref assistant.InboundHandoffRef, absorberID string) (bool, bool, error) {
	defer s.observeStorage(ctx, "ReleaseInboundHandoff", "write")()
	if s == nil || s.db == nil || !inboundHandoffRefUsable(ref) || strings.TrimSpace(absorberID) == "" {
		return false, false, nil
	}
	now := time.Now().UTC().UnixNano()
	handedOff := `(status = '` + inboundStatusDone + `' AND outcome = '` + assistantOutcomeHandedOffPending + `')`
	where, whereArgs := inboundHandoffRow(ref)
	args := []any{inboundStatusPending, assistant.InboundPriorityTriggered, assistant.InboundPriorityTriggered, now, now}
	args = append(args, whereArgs...)
	args = append(args, strings.TrimSpace(absorberID), inboundHandoffPending)
	var requeued int
	err := s.db.QueryRowContext(ctx, `
UPDATE inbound_events
SET superseded_by = NULL, handoff_state = NULL, handoff_at = NULL,
    status = CASE WHEN `+handedOff+` THEN ? ELSE status END,
    priority = CASE WHEN `+handedOff+` AND priority < ? THEN ? ELSE priority END,
    available_at = CASE WHEN `+handedOff+` THEN ? ELSE available_at END,
    outcome = CASE WHEN `+handedOff+` THEN NULL ELSE outcome END,
    completed_at = CASE WHEN `+handedOff+` THEN NULL ELSE completed_at END,
    updated_at = ?
WHERE `+where+`
  AND superseded_by = ? AND handoff_state = ?
RETURNING CASE WHEN status = '`+inboundStatusPending+`' AND completed_at IS NULL AND outcome IS NULL THEN 1 ELSE 0 END
`, args...).Scan(&requeued)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, false, nil
		}
		return false, false, fmt.Errorf("release inbound handoff: %w", err)
	}
	return true, requeued == 1, nil
}

// CompleteInboundHandoff 是交出去那一轮的收尾：落终态并写上交接状态。接手那一轮的
// 落定要是抢在它前面，已经是 final 的不会被改回 pending，结果照写 superseded_follow_up。
func (s *SQLiteStore) CompleteInboundHandoff(ctx context.Context, id string, leaseOwner string, absorberID string, final bool) error {
	defer s.observeStorage(ctx, "CompleteInboundHandoff", "write")()
	if s == nil || s.db == nil {
		return fmt.Errorf("complete inbound handoff: sqlite store is not configured")
	}
	id, leaseOwner, absorberID = strings.TrimSpace(id), strings.TrimSpace(leaseOwner), strings.TrimSpace(absorberID)
	if id == "" || leaseOwner == "" {
		return fmt.Errorf("complete inbound handoff: id and lease owner are required")
	}
	outcome, state := assistantOutcomeHandedOffPending, inboundHandoffPending
	if final {
		outcome, state = "superseded_follow_up", inboundHandoffFinal
	}
	alreadyFinal := `(handoff_state = '` + inboundHandoffFinal + `' AND superseded_by = ?)`
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, `
UPDATE inbound_events
SET status = ?,
    outcome = CASE WHEN `+alreadyFinal+` THEN 'superseded_follow_up' ELSE ? END,
    last_error = NULL,
    handoff_state = CASE WHEN `+alreadyFinal+` THEN '`+inboundHandoffFinal+`' ELSE ? END,
    superseded_by = CASE WHEN ? != '' THEN ? ELSE superseded_by END,
    handoff_at = COALESCE(handoff_at, ?),
    lease_owner = NULL, lease_until = NULL,
    completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND lease_owner = ?
`, inboundStatusDone, absorberID, outcome, absorberID, state, absorberID, absorberID, now, now, now, id, inboundStatusProcessing, leaseOwner)
	if err != nil {
		return fmt.Errorf("complete inbound handoff %q: %w", id, err)
	}
	return requireInboundLeaseUpdate(result, "complete handoff", id)
}

func (s *SQLiteStore) ListPendingInboundHandoffs(ctx context.Context, limit int) ([]assistant.InboundHandoff, error) {
	defer s.observeStorage(ctx, "ListPendingInboundHandoffs", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, payload, COALESCE(superseded_by, ''), COALESCE(handoff_at, 0)
FROM inbound_events
WHERE handoff_state = ?
ORDER BY handoff_at ASC
LIMIT ?
`, inboundHandoffPending, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending inbound handoffs: %w", err)
	}
	defer rows.Close()
	var handoffs []assistant.InboundHandoff
	for rows.Next() {
		var id, payload, absorberID string
		var markedAt int64
		if err := rows.Scan(&id, &payload, &absorberID, &markedAt); err != nil {
			return nil, fmt.Errorf("scan pending inbound handoff: %w", err)
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		handoff := assistant.InboundHandoff{InboundHandoffRef: assistant.InboundHandoffRef{ID: id, Event: event}, AbsorberID: absorberID}
		if markedAt > 0 {
			handoff.MarkedAt = time.Unix(0, markedAt)
		}
		handoffs = append(handoffs, handoff)
	}
	return handoffs, rows.Err()
}
