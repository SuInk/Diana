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

// 风格笔记：每个机器人、每个群一行「这个群怎么说话」。manual 标记主人手动改过，
// 自动学习不再覆盖它。
//
// 旧的表达学习按整句计数，存在 group_expressions 表里。那些计数都是从聊天里算出来的，
// 新版不再读，这里顺手把表删掉，免得留一张没人维护的表。
const groupStyleSchema = `
CREATE TABLE IF NOT EXISTS group_styles (
  profile_id TEXT NOT NULL,
  group_id TEXT NOT NULL,
  text TEXT NOT NULL,
  manual INTEGER NOT NULL DEFAULT 0,
  sample_count INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (profile_id, group_id)
);
DROP INDEX IF EXISTS idx_group_expressions_scope_seen;
DROP TABLE IF EXISTS group_expressions;
`

func (s *SQLiteStore) migrateGroupStyles() error {
	if _, err := s.db.Exec(groupStyleSchema); err != nil {
		return fmt.Errorf("create group style schema: %w", err)
	}
	return nil
}

// GroupStyle 读一个群的风格笔记。
func (s *SQLiteStore) GroupStyle(ctx context.Context, profileID, groupID string) (assistant.GroupStyle, bool, error) {
	if s == nil || s.db == nil {
		return assistant.GroupStyle{}, false, nil
	}
	style := assistant.GroupStyle{ProfileID: strings.TrimSpace(profileID), GroupID: strings.TrimSpace(groupID)}
	var manual int
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT text, manual, sample_count, updated_at FROM group_styles WHERE profile_id = ? AND group_id = ?`,
		style.ProfileID, style.GroupID).Scan(&style.Text, &manual, &style.SampleCount, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.GroupStyle{}, false, nil
	}
	if err != nil {
		return assistant.GroupStyle{}, false, err
	}
	style.Manual = manual != 0
	style.UpdatedAt = time.Unix(updated, 0)
	return style, true, nil
}

// SaveGroupStyle 写一个群的风格笔记，已有就覆盖。
func (s *SQLiteStore) SaveGroupStyle(ctx context.Context, style assistant.GroupStyle) error {
	if s == nil || s.db == nil {
		return nil
	}
	manual := 0
	if style.Manual {
		manual = 1
	}
	updated := style.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO group_styles (profile_id, group_id, text, manual, sample_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(profile_id, group_id) DO UPDATE SET
  text = excluded.text,
  manual = excluded.manual,
  sample_count = excluded.sample_count,
  updated_at = excluded.updated_at
`, strings.TrimSpace(style.ProfileID), strings.TrimSpace(style.GroupID), style.Text, manual, style.SampleCount, updated.Unix())
	return err
}

// DeleteGroupStyle 删掉一个群的风格笔记，交回自动学习。
func (s *SQLiteStore) DeleteGroupStyle(ctx context.Context, profileID, groupID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM group_styles WHERE profile_id = ? AND group_id = ?`, strings.TrimSpace(profileID), strings.TrimSpace(groupID))
	return err
}
