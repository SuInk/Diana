// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

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

// 少实现一个方法不会报错，只会让 runtime 里那个类型断言悄悄失败——连带把
// InboundEventSuperseded 一起停掉。编译期钉住，别让它变成运行时才发现的事。
var _ assistant.InboundMediaTurnStore = (*SQLiteStore)(nil)

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

// PeekInboundMediaForTurn 返回同一批相邻媒体，但不动任何状态。
//
// 认领是不可逆的：媒体那条会被标成 superseded，自己的任务当场作废。所以「这条
// 文本到底在指谁」得先判完再认领，判断期间不能已经把人家注销了。这个只读版本
// 就是给那一步用的。
func (s *SQLiteStore) PeekInboundMediaForTurn(ctx context.Context, currentID, session string, event assistant.MessageEvent, window time.Duration) ([]assistant.MessageEvent, error) {
	candidates, err := s.inboundMediaCandidates(ctx, currentID, session, event, window)
	if err != nil {
		return nil, err
	}
	peeked := make([]assistant.MessageEvent, 0, len(candidates))
	for _, candidate := range candidates {
		peeked = append(peeked, candidate.event)
	}
	sort.SliceStable(peeked, func(i, j int) bool { return peeked[i].Time < peeked[j].Time })
	return peeked, nil
}

// inboundMediaCandidates 找出可以并进这一轮的相邻媒体，按离锚点的远近排序。
// 只读，调用方决定要不要认领。
func (s *SQLiteStore) inboundMediaCandidates(ctx context.Context, currentID, session string, event assistant.MessageEvent, window time.Duration) ([]inboundMediaCandidate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("inbound media candidates: sqlite store is not configured")
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
	rows, err := s.db.QueryContext(ctx, `
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
	defer func() { _ = rows.Close() }()
	var candidates []inboundMediaCandidate
	for rows.Next() {
		var candidate inboundMediaCandidate
		var payload string
		if err := rows.Scan(&candidate.id, &candidate.status, &candidate.eventTime, &payload); err != nil {
			return nil, fmt.Errorf("scan inbound media candidate: %w", err)
		}
		if json.Unmarshal([]byte(payload), &candidate.event) == nil && assistant.EventIsMergeableMediaOnly(candidate.event) {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbound media candidates: %w", err)
	}
	return inboundMediaBatch(candidates), nil
}

// inboundMediaBatch 只保留和最近那条属于同一批（连发）的媒体。
func inboundMediaBatch(candidates []inboundMediaCandidate) []inboundMediaCandidate {
	if len(candidates) == 0 {
		return nil
	}
	nearestTime := candidates[0].eventTime
	slack := int64(inboundMediaBatchSlack / time.Second)
	batch := make([]inboundMediaCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.eventTime < nearestTime-slack || candidate.eventTime > nearestTime+slack {
			continue
		}
		batch = append(batch, candidate)
	}
	return batch
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

	// 事务内重新查一遍而不是复用 PeekInboundMediaForTurn：认领必须和读在同一个
	// 事务里，否则两轮文本可能同时看到同一条媒体、都以为自己抢到了。
	now := time.Now().UTC().UnixNano()
	claimed := make([]assistant.MessageEvent, 0, len(candidates))
	for _, candidate := range inboundMediaBatch(candidates) {
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
