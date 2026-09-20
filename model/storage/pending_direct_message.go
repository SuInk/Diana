// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/google/uuid"
)

func (s *SQLiteStore) SavePendingDirectMessage(ctx context.Context, item assistant.PendingDirectMessage) (assistant.PendingDirectMessage, error) {
	item.ProfileID = strings.TrimSpace(item.ProfileID)
	item.Platform = strings.TrimSpace(item.Platform)
	item.UserID = strings.TrimSpace(item.UserID)
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.ExpiresAt.IsZero() {
		item.ExpiresAt = item.CreatedAt.Add(7 * 24 * time.Hour)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO pending_direct_messages (
  id, profile_id, platform, user_id, source_session, message, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, item.ID, item.ProfileID, item.Platform, item.UserID, item.SourceSession, item.Message,
		item.CreatedAt.UnixNano(), item.ExpiresAt.UnixNano())
	if err != nil {
		return assistant.PendingDirectMessage{}, err
	}
	return item, nil
}

func (s *SQLiteStore) CountPendingDirectMessages(ctx context.Context, profileID, userID string, now time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pending_direct_messages
WHERE profile_id = ? AND user_id = ? AND expires_at > ?
`, strings.TrimSpace(profileID), strings.TrimSpace(userID), now.UnixNano()).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// TakePendingDirectMessages 在一个事务里读出再删掉：好友通知和主人审批可能前后脚
// 到达，两条路各读一次就会把同一段话发两遍。
func (s *SQLiteStore) TakePendingDirectMessages(ctx context.Context, profileID, userID string, now time.Time) ([]assistant.PendingDirectMessage, error) {
	profileID = strings.TrimSpace(profileID)
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT id, profile_id, COALESCE(platform,''), user_id, COALESCE(source_session,''), message, created_at, expires_at
FROM pending_direct_messages
WHERE profile_id = ? AND user_id = ? AND expires_at > ?
ORDER BY created_at ASC
`, profileID, userID, now.UnixNano())
	if err != nil {
		return nil, err
	}
	items, err := scanPendingDirectMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, tx.Commit()
	}
	// 过期的那些顺手一起清掉：这个人的抽屉打开了，不必留到下一轮全局清理。
	if _, err := tx.ExecContext(ctx, `
DELETE FROM pending_direct_messages WHERE profile_id = ? AND user_id = ?
`, profileID, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *SQLiteStore) PurgeExpiredPendingDirectMessages(ctx context.Context, now time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
DELETE FROM pending_direct_messages WHERE expires_at <= ?
`, now.UnixNano())
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func scanPendingDirectMessages(rows *sql.Rows) ([]assistant.PendingDirectMessage, error) {
	defer rows.Close()
	var items []assistant.PendingDirectMessage
	for rows.Next() {
		var item assistant.PendingDirectMessage
		var createdAt, expiresAt int64
		if err := rows.Scan(&item.ID, &item.ProfileID, &item.Platform, &item.UserID,
			&item.SourceSession, &item.Message, &createdAt, &expiresAt); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(0, createdAt)
		item.ExpiresAt = time.Unix(0, expiresAt)
		items = append(items, item)
	}
	return items, rows.Err()
}
