// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// media_generation_usage 按天记成功生成的次数，一行是某天某个群或某个人的合计。
// 不从 app_logs 里数：操作日志会按保留期清理，也不带机器人归属，拿它当限额的
// 账本，清一次日志当天的次数就归零了。
const mediaGenerationUsageSchema = `
CREATE TABLE IF NOT EXISTS media_generation_usage (
  profile_id TEXT NOT NULL,
  platform TEXT NOT NULL,
  kind TEXT NOT NULL,
  day TEXT NOT NULL,
  scope TEXT NOT NULL,
  subject_id TEXT NOT NULL,
  count INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (profile_id, platform, kind, day, scope, subject_id)
);
`

// mediaGenerationUsageKeepDays 是计数保留的天数。限额只看当天，多留一个月方便
// 排查「昨天是不是真用完了」。
const mediaGenerationUsageKeepDays = 31

const (
	mediaGenerationScopeGroup = "group"
	mediaGenerationScopeUser  = "user"
)

func (s *SQLiteStore) migrateMediaGenerationUsage() error {
	if _, err := s.db.Exec(mediaGenerationUsageSchema); err != nil {
		return fmt.Errorf("create media generation usage schema: %w", err)
	}
	return nil
}

// MediaGenerationCounts 读出某天这个群、这个人已经成功生成的次数。
func (s *SQLiteStore) MediaGenerationCounts(ctx context.Context, key assistant.MediaGenerationKey) (assistant.MediaGenerationCounts, error) {
	defer s.observeStorage(ctx, "MediaGenerationCounts", "read")()
	var counts assistant.MediaGenerationCounts
	if s == nil || s.db == nil {
		return counts, fmt.Errorf("media generation usage storage unavailable")
	}
	rows, err := s.eventReader().QueryContext(ctx, `SELECT scope, count FROM media_generation_usage
WHERE profile_id = ? AND platform = ? AND kind = ? AND day = ?
AND ((scope = ? AND subject_id = ?) OR (scope = ? AND subject_id = ?))`,
		key.ProfileID, key.Platform, string(key.Kind), key.Day,
		mediaGenerationScopeGroup, strings.TrimSpace(key.GroupID), mediaGenerationScopeUser, strings.TrimSpace(key.UserID))
	if err != nil {
		return counts, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope string
		var count int64
		if err := rows.Scan(&scope, &count); err != nil {
			return counts, err
		}
		switch {
		case scope == mediaGenerationScopeGroup && strings.TrimSpace(key.GroupID) != "":
			counts.Group = count
		case scope == mediaGenerationScopeUser && strings.TrimSpace(key.UserID) != "":
			counts.User = count
		}
	}
	return counts, rows.Err()
}

// AddMediaGeneration 给这个群和这个人各加上 count 次，顺手清掉过了保留期的天。
func (s *SQLiteStore) AddMediaGeneration(ctx context.Context, key assistant.MediaGenerationKey, count int64) error {
	defer s.observeStorage(ctx, "AddMediaGeneration", "write")()
	if s == nil || s.db == nil {
		return fmt.Errorf("media generation usage storage unavailable")
	}
	if count <= 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for _, subject := range []struct{ scope, id string }{
		{mediaGenerationScopeGroup, strings.TrimSpace(key.GroupID)},
		{mediaGenerationScopeUser, strings.TrimSpace(key.UserID)},
	} {
		if subject.id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_generation_usage (profile_id, platform, kind, day, scope, subject_id, count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(profile_id, platform, kind, day, scope, subject_id) DO UPDATE SET
  count = media_generation_usage.count + excluded.count,
  updated_at = excluded.updated_at`,
			key.ProfileID, key.Platform, string(key.Kind), key.Day, subject.scope, subject.id, count, now); err != nil {
			return err
		}
	}
	if day, err := time.Parse(time.DateOnly, key.Day); err == nil {
		cutoff := day.AddDate(0, 0, -mediaGenerationUsageKeepDays).Format(time.DateOnly)
		if _, err := tx.ExecContext(ctx, `DELETE FROM media_generation_usage WHERE day < ?`, cutoff); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var _ assistant.MediaGenerationUsageStore = (*SQLiteStore)(nil)
