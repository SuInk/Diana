package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

const inboundMediaBatchSlack = 3 * time.Second

type inboundMediaCandidate struct {
	id        string
	status    string
	eventTime int64
	event     assistant.MessageEvent
}

func inboundInitialAvailableAt(event assistant.MessageEvent, now time.Time) time.Time {
	if assistant.EventHasDirectMediaReference(event) && !assistant.EventIsMergeableMediaOnly(event) {
		return now
	}
	if !assistant.EventIsMergeableMediaOnly(event) {
		return now
	}
	return now.Add(assistant.InboundMediaMergeWindow)
}

// ClaimInboundMediaForTurn atomically marks adjacent media as consumed by the
// current textual turn. Processing rows keep their lease but gain a send-time
// supersession marker, while pending rows become terminal immediately.
func (s *SQLiteStore) ClaimInboundMediaForTurn(ctx context.Context, currentID, session string, event assistant.MessageEvent, window time.Duration) ([]assistant.MessageEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("claim inbound media turn: sqlite store is not configured")
	}
	currentID = strings.TrimSpace(currentID)
	session = strings.TrimSpace(session)
	if currentID == "" || session == "" || strings.TrimSpace(event.UserID) == "" || window <= 0 {
		return nil, nil
	}
	anchor := event.Time
	if anchor <= 0 {
		anchor = time.Now().Unix()
	}
	delta := int64(window / time.Second)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin inbound media claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT id, status, event_time, payload
FROM inbound_events
WHERE session = ? AND COALESCE(user_id, '') = ? AND id <> ?
  AND status IN (?, ?) AND superseded_by IS NULL
  AND event_time BETWEEN ? AND ?
ORDER BY ABS(event_time - ?) ASC, event_time ASC, created_at ASC, id ASC
`, session, strings.TrimSpace(event.UserID), currentID, inboundStatusPending, inboundStatusProcessing, anchor-delta, anchor+delta, anchor)
	if err != nil {
		return nil, fmt.Errorf("query inbound media candidates: %w", err)
	}
	var candidates []inboundMediaCandidate
	for rows.Next() {
		var candidate inboundMediaCandidate
		var payload string
		if err := rows.Scan(&candidate.id, &candidate.status, &candidate.eventTime, &payload); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan inbound media candidate: %w", err)
		}
		if json.Unmarshal([]byte(payload), &candidate.event) == nil && assistant.EventIsMergeableMediaOnly(candidate.event) {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close inbound media candidates: %w", err)
	}
	if len(candidates) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty inbound media claim: %w", err)
		}
		return nil, nil
	}

	nearestTime := candidates[0].eventTime
	batchSlack := int64(inboundMediaBatchSlack / time.Second)
	now := time.Now().UTC().UnixNano()
	claimed := make([]assistant.MessageEvent, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.eventTime < nearestTime-batchSlack || candidate.eventTime > nearestTime+batchSlack {
			continue
		}
		var result sql.Result
		if candidate.status == inboundStatusPending {
			result, err = tx.ExecContext(ctx, `
UPDATE inbound_events
SET status = ?, outcome = 'superseded_media_turn', superseded_by = ?,
    completed_at = ?, updated_at = ?
WHERE id = ? AND status = ? AND superseded_by IS NULL
`, inboundStatusDone, currentID, now, now, candidate.id, inboundStatusPending)
		} else {
			result, err = tx.ExecContext(ctx, `
UPDATE inbound_events SET superseded_by = ?, updated_at = ?
WHERE id = ? AND status = ? AND superseded_by IS NULL
`, currentID, now, candidate.id, inboundStatusProcessing)
		}
		if err != nil {
			return nil, fmt.Errorf("supersede inbound media %q: %w", candidate.id, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("inspect inbound media claim %q: %w", candidate.id, err)
		}
		if updated > 0 {
			claimed = append(claimed, candidate.event)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit inbound media claim: %w", err)
	}
	sort.SliceStable(claimed, func(i, j int) bool { return claimed[i].Time < claimed[j].Time })
	return claimed, nil
}

func (s *SQLiteStore) InboundEventSuperseded(ctx context.Context, event assistant.MessageEvent) (string, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(event.MessageID) == "" {
		return "", false, nil
	}
	var supersededBy sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT superseded_by
FROM inbound_events
WHERE message_id = ? AND kind = ?
  AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
ORDER BY created_at DESC, id DESC LIMIT 1
`, strings.TrimSpace(event.MessageID), string(event.Kind), strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID)).Scan(&supersededBy)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("check inbound event supersession: %w", err)
	}
	value := strings.TrimSpace(supersededBy.String)
	return value, value != "", nil
}
