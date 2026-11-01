// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) migrateRoom() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS room_intents (
	profile_id TEXT PRIMARY KEY CHECK (length(trim(profile_id)) > 0),
	pose TEXT NOT NULL CHECK (pose IN ('rest','phone','desk')),
	updated_at INTEGER NOT NULL
	)`)
	return err
}

func (s *SQLiteStore) LoadRoomIntent(ctx context.Context, profile string) (assistant.RoomIntent, error) {
	if strings.TrimSpace(profile) == "" {
		return assistant.RoomIntent{}, fmt.Errorf("room requires profile")
	}
	var intent assistant.RoomIntent
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT pose, updated_at FROM room_intents WHERE profile_id = ?`, profile).Scan(&intent.Pose, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.RoomIntent{Pose: "rest"}, nil
	}
	if err != nil {
		return assistant.RoomIntent{}, err
	}
	intent.UpdatedAt = time.Unix(0, at)
	return intent, nil
}

func (s *SQLiteStore) SaveRoomIntent(ctx context.Context, profile string, intent assistant.RoomIntent) error {
	if strings.TrimSpace(profile) == "" || !assistant.ValidRoomPose(intent.Pose) || intent.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid room intent")
	}
	tx, err := s.beginWriteTx(ctx, "SaveRoomIntent")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO room_intents(profile_id, pose, updated_at) VALUES(?,?,?)
	ON CONFLICT(profile_id) DO UPDATE SET pose=excluded.pose, updated_at=excluded.updated_at`, profile, intent.Pose, intent.UpdatedAt.UnixNano())
	if err != nil {
		return err
	}
	return tx.Commit()
}
