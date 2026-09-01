// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/google/uuid"
)

func (s *SQLiteStore) PutThreadState(ctx context.Context, request assistant.ThreadStatePutRequest) (assistant.ThreadState, error) {
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	request.Session = strings.TrimSpace(request.Session)
	request.UserID = strings.TrimSpace(request.UserID)
	request.TaskKind = strings.TrimSpace(request.TaskKind)
	if request.Session == "" || request.UserID == "" || request.TaskKind == "" {
		return assistant.ThreadState{}, fmt.Errorf("thread state scope is incomplete")
	}
	if !json.Valid(request.State) {
		return assistant.ThreadState{}, fmt.Errorf("thread state payload is not valid JSON")
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	if request.ExpiresAt.IsZero() || !request.ExpiresAt.After(request.Now) {
		return assistant.ThreadState{}, fmt.Errorf("thread state expiry must be after now")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.ThreadState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	nowNS := request.Now.UnixNano()
	if _, err := tx.ExecContext(ctx, `
UPDATE thread_states
SET status = 'expired', state_json = '{}', updated_at = ?
WHERE status = 'active' AND expires_at <= ?
`, nowNS, nowNS); err != nil {
		return assistant.ThreadState{}, err
	}

	var id string
	var version int
	var createdAtNS int64
	err = tx.QueryRowContext(ctx, `
SELECT id, version, created_at
FROM thread_states
WHERE profile_id = ? AND session = ? AND user_id = ? AND task_kind = ? AND status = 'active'
`, request.ProfileID, request.Session, request.UserID, request.TaskKind).Scan(&id, &version, &createdAtNS)
	switch {
	case err == nil:
		if request.ExpectedVersion > 0 && request.ExpectedVersion != version {
			return assistant.ThreadState{}, fmt.Errorf("%w: current=%d expected=%d", assistant.ErrThreadStateVersionConflict, version, request.ExpectedVersion)
		}
		nextVersion := version + 1
		result, err := tx.ExecContext(ctx, `
UPDATE thread_states
SET state_json = ?, version = ?, source_message_id = ?, updated_at = ?, expires_at = ?
WHERE id = ? AND status = 'active' AND version = ?
`, string(request.State), nextVersion, request.SourceMessageID, nowNS, request.ExpiresAt.UnixNano(), id, version)
		if err != nil {
			return assistant.ThreadState{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return assistant.ThreadState{}, assistant.ErrThreadStateVersionConflict
		}
		version = nextVersion
	case errors.Is(err, sql.ErrNoRows):
		if request.ExpectedVersion > 0 {
			return assistant.ThreadState{}, fmt.Errorf("%w: state does not exist", assistant.ErrThreadStateVersionConflict)
		}
		id = uuid.NewString()
		version = 1
		createdAtNS = nowNS
		_, err = tx.ExecContext(ctx, `
INSERT INTO thread_states (
  id, profile_id, session, user_id, task_kind, state_json, version, status,
  source_message_id, created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, 1, 'active', ?, ?, ?, ?)
`, id, request.ProfileID, request.Session, request.UserID, request.TaskKind, string(request.State), request.SourceMessageID, nowNS, nowNS, request.ExpiresAt.UnixNano())
		if err != nil {
			return assistant.ThreadState{}, err
		}
	default:
		return assistant.ThreadState{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.ThreadState{}, err
	}
	return assistant.ThreadState{
		ID: id, ProfileID: request.ProfileID, Session: request.Session, UserID: request.UserID,
		TaskKind: request.TaskKind, State: append(json.RawMessage(nil), request.State...), Version: version,
		Status: assistant.ThreadStateActive, SourceMessageID: request.SourceMessageID,
		CreatedAt: time.Unix(0, createdAtNS), UpdatedAt: request.Now, ExpiresAt: request.ExpiresAt,
	}, nil
}

func (s *SQLiteStore) ListActiveThreadStates(ctx context.Context, profileID, session, userID string, now time.Time, limit int) ([]assistant.ThreadState, error) {
	profileID = strings.TrimSpace(profileID)
	session = strings.TrimSpace(session)
	userID = strings.TrimSpace(userID)
	if session == "" || userID == "" {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 || limit > 20 {
		limit = 4
	}
	nowNS := now.UnixNano()
	var expired bool
	if err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM thread_states WHERE status = 'active' AND expires_at <= ?)
`, nowNS).Scan(&expired); err != nil {
		return nil, err
	}
	if expired {
		if _, err := s.db.ExecContext(ctx, `
UPDATE thread_states
SET status = 'expired', state_json = '{}', updated_at = ?
WHERE status = 'active' AND expires_at <= ?
`, nowNS, nowNS); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, profile_id, session, user_id, task_kind, state_json, version, status,
       COALESCE(source_message_id, ''), created_at, updated_at, expires_at
FROM thread_states
WHERE profile_id = ? AND session = ? AND user_id = ? AND status = 'active' AND expires_at > ?
ORDER BY updated_at DESC, id DESC
LIMIT ?
`, profileID, session, userID, nowNS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]assistant.ThreadState, 0, limit)
	for rows.Next() {
		item, err := scanThreadState(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) EndThreadState(ctx context.Context, request assistant.ThreadStateEndRequest) (assistant.ThreadState, error) {
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	request.Session = strings.TrimSpace(request.Session)
	request.UserID = strings.TrimSpace(request.UserID)
	request.TaskKind = strings.TrimSpace(request.TaskKind)
	if request.Status != assistant.ThreadStateCompleted && request.Status != assistant.ThreadStateCancelled {
		return assistant.ThreadState{}, fmt.Errorf("invalid terminal thread state status %q", request.Status)
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.ThreadState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	nowNS := request.Now.UnixNano()
	if _, err := tx.ExecContext(ctx, `
UPDATE thread_states
SET status = 'expired', state_json = '{}', updated_at = ?
WHERE status = 'active' AND expires_at <= ?
`, nowNS, nowNS); err != nil {
		return assistant.ThreadState{}, err
	}
	row := tx.QueryRowContext(ctx, `
SELECT id, profile_id, session, user_id, task_kind, state_json, version, status,
       COALESCE(source_message_id, ''), created_at, updated_at, expires_at
FROM thread_states
WHERE profile_id = ? AND session = ? AND user_id = ? AND task_kind = ? AND status = 'active'
`, request.ProfileID, request.Session, request.UserID, request.TaskKind)
	item, err := scanThreadState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.ThreadState{}, fmt.Errorf("没有进行中的 %s 任务", request.TaskKind)
	}
	if err != nil {
		return assistant.ThreadState{}, err
	}
	if request.ExpectedVersion > 0 && request.ExpectedVersion != item.Version {
		return assistant.ThreadState{}, fmt.Errorf("%w: current=%d expected=%d", assistant.ErrThreadStateVersionConflict, item.Version, request.ExpectedVersion)
	}
	nextVersion := item.Version + 1
	result, err := tx.ExecContext(ctx, `
UPDATE thread_states
SET state_json = '{}', version = ?, status = ?, updated_at = ?
WHERE id = ? AND status = 'active' AND version = ?
	`, nextVersion, string(request.Status), nowNS, item.ID, item.Version)
	if err != nil {
		return assistant.ThreadState{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return assistant.ThreadState{}, assistant.ErrThreadStateVersionConflict
	}
	if err := tx.Commit(); err != nil {
		return assistant.ThreadState{}, err
	}
	item.State = nil
	item.Version = nextVersion
	item.Status = request.Status
	item.UpdatedAt = request.Now
	return item, nil
}

type threadStateScanner interface {
	Scan(...any) error
}

func scanThreadState(scanner threadStateScanner) (assistant.ThreadState, error) {
	var item assistant.ThreadState
	var stateJSON string
	var status string
	var createdAtNS, updatedAtNS, expiresAtNS int64
	err := scanner.Scan(
		&item.ID, &item.ProfileID, &item.Session, &item.UserID, &item.TaskKind, &stateJSON,
		&item.Version, &status, &item.SourceMessageID, &createdAtNS, &updatedAtNS, &expiresAtNS,
	)
	if err != nil {
		return assistant.ThreadState{}, err
	}
	item.State = json.RawMessage(stateJSON)
	item.Status = assistant.ThreadStateStatus(status)
	item.CreatedAt = time.Unix(0, createdAtNS)
	item.UpdatedAt = time.Unix(0, updatedAtNS)
	item.ExpiresAt = time.Unix(0, expiresAtNS)
	return item, nil
}
