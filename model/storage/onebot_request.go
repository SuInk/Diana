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
	"github.com/google/uuid"
)

func (s *SQLiteStore) SaveOneBotRequest(ctx context.Context, item assistant.OneBotRequestRecord) (assistant.OneBotRequestRecord, bool, error) {
	item.ProfileID = strings.TrimSpace(item.ProfileID)
	item.Platform = strings.TrimSpace(item.Platform)
	item.RequestType = strings.ToLower(strings.TrimSpace(item.RequestType))
	item.SubType = strings.ToLower(strings.TrimSpace(item.SubType))
	item.Flag = strings.TrimSpace(item.Flag)
	if item.RequestType != "friend" && item.RequestType != "group" {
		return assistant.OneBotRequestRecord{}, false, fmt.Errorf("unsupported OneBot request type %q", item.RequestType)
	}
	if item.Flag == "" {
		return assistant.OneBotRequestRecord{}, false, fmt.Errorf("OneBot request flag is empty")
	}
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Status == "" {
		item.Status = assistant.OneBotRequestPending
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO onebot_requests (
  id, profile_id, platform, self_id, request_type, sub_type, user_id, group_id,
  comment, flag, status, reason, created_at, updated_at, decided_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
`, item.ID, item.ProfileID, item.Platform, item.SelfID, item.RequestType, item.SubType, item.UserID, item.GroupID,
		item.Comment, item.Flag, string(item.Status), item.Reason, item.CreatedAt.UnixNano(), item.UpdatedAt.UnixNano())
	if err != nil {
		return assistant.OneBotRequestRecord{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return assistant.OneBotRequestRecord{}, false, err
	}
	stored, found, err := s.getOneBotRequestByIdentity(ctx, item.ProfileID, item.RequestType, item.SubType, item.Flag)
	if err != nil {
		return assistant.OneBotRequestRecord{}, false, err
	}
	if !found {
		return assistant.OneBotRequestRecord{}, false, fmt.Errorf("saved OneBot request could not be reloaded")
	}
	return stored, rows == 1, nil
}

func (s *SQLiteStore) ListOneBotRequests(ctx context.Context, profileID string, status assistant.OneBotRequestStatus, limit int) ([]assistant.OneBotRequestRecord, error) {
	profileID = strings.TrimSpace(profileID)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := `
SELECT id, profile_id, platform, COALESCE(self_id,''), request_type, COALESCE(sub_type,''),
       COALESCE(user_id,''), COALESCE(group_id,''), COALESCE(comment,''), flag, status,
       COALESCE(reason,''), created_at, updated_at, decided_at
FROM onebot_requests
WHERE profile_id = ?`
	args := []any{profileID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]assistant.OneBotRequestRecord, 0, limit)
	for rows.Next() {
		item, err := scanOneBotRequest(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetOneBotRequest(ctx context.Context, profileID, id string) (assistant.OneBotRequestRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, profile_id, platform, COALESCE(self_id,''), request_type, COALESCE(sub_type,''),
       COALESCE(user_id,''), COALESCE(group_id,''), COALESCE(comment,''), flag, status,
       COALESCE(reason,''), created_at, updated_at, decided_at
FROM onebot_requests WHERE profile_id = ? AND id = ?
`, strings.TrimSpace(profileID), strings.TrimSpace(id))
	item, err := scanOneBotRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.OneBotRequestRecord{}, false, nil
	}
	return item, err == nil, err
}

func (s *SQLiteStore) ResolveOneBotRequest(ctx context.Context, profileID, id string, status assistant.OneBotRequestStatus, reason string, decidedAt time.Time) (assistant.OneBotRequestRecord, error) {
	if status != assistant.OneBotRequestApproved && status != assistant.OneBotRequestRejected {
		return assistant.OneBotRequestRecord{}, fmt.Errorf("invalid OneBot request status %q", status)
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now()
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE onebot_requests
SET status = ?, reason = ?, updated_at = ?, decided_at = ?
WHERE profile_id = ? AND id = ? AND status = 'pending'
`, string(status), strings.TrimSpace(reason), decidedAt.UnixNano(), decidedAt.UnixNano(), strings.TrimSpace(profileID), strings.TrimSpace(id))
	if err != nil {
		return assistant.OneBotRequestRecord{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return assistant.OneBotRequestRecord{}, err
	}
	item, found, err := s.GetOneBotRequest(ctx, profileID, id)
	if err != nil {
		return assistant.OneBotRequestRecord{}, err
	}
	if !found {
		return assistant.OneBotRequestRecord{}, fmt.Errorf("OneBot request %s not found", id)
	}
	if rows == 0 && item.Status != status {
		return assistant.OneBotRequestRecord{}, fmt.Errorf("OneBot request %s was already %s", id, item.Status)
	}
	return item, nil
}

func (s *SQLiteStore) getOneBotRequestByIdentity(ctx context.Context, profileID, requestType, subType, flag string) (assistant.OneBotRequestRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, profile_id, platform, COALESCE(self_id,''), request_type, COALESCE(sub_type,''),
       COALESCE(user_id,''), COALESCE(group_id,''), COALESCE(comment,''), flag, status,
       COALESCE(reason,''), created_at, updated_at, decided_at
FROM onebot_requests
WHERE profile_id = ? AND request_type = ? AND sub_type = ? AND flag = ?
`, profileID, requestType, subType, flag)
	item, err := scanOneBotRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.OneBotRequestRecord{}, false, nil
	}
	return item, err == nil, err
}

type oneBotRequestScanner interface {
	Scan(...any) error
}

func scanOneBotRequest(scanner oneBotRequestScanner) (assistant.OneBotRequestRecord, error) {
	var item assistant.OneBotRequestRecord
	var status string
	var createdAtNS, updatedAtNS int64
	var decidedAtNS sql.NullInt64
	err := scanner.Scan(
		&item.ID, &item.ProfileID, &item.Platform, &item.SelfID, &item.RequestType, &item.SubType,
		&item.UserID, &item.GroupID, &item.Comment, &item.Flag, &status, &item.Reason,
		&createdAtNS, &updatedAtNS, &decidedAtNS,
	)
	if err != nil {
		return assistant.OneBotRequestRecord{}, err
	}
	item.Status = assistant.OneBotRequestStatus(status)
	item.CreatedAt = time.Unix(0, createdAtNS)
	item.UpdatedAt = time.Unix(0, updatedAtNS)
	if decidedAtNS.Valid {
		item.DecidedAt = time.Unix(0, decidedAtNS.Int64)
	}
	return item, nil
}
