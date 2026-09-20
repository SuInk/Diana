// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

var ErrUserMemoryConflict = errors.New("人员记录已更新或删除，请刷新后重试")

// EditUserMemory changes only editable fields, with a revision check shared by deletion.
// The profile ID is always an exact match, including legacy records with an empty ID.
func (s *SQLiteStore) EditUserMemory(ctx context.Context, profileID, userID string, edit assistant.UserMemoryProfile, remove bool) error {
	s.userMemoryMu.Lock()
	defer s.userMemoryMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var before int
	var revision string
	err = tx.QueryRowContext(ctx, "SELECT favorability, updated_at FROM user_profiles WHERE bot_profile_id = ? AND user_id = ?", profileID, userID).Scan(&before, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserMemoryConflict
	}
	if err != nil {
		return err
	}
	if revision != edit.UpdatedAt.Format(time.RFC3339Nano) {
		return ErrUserMemoryConflict
	}
	if remove {
		if _, err = tx.ExecContext(ctx, "DELETE FROM user_favorability_changes WHERE bot_profile_id = ? AND user_id = ?", profileID, userID); err != nil {
			return err
		}
		// 结构化长期记忆也要一起收掉。以前只删 user_profiles，控制台上人没了、
		// 机器人却还记得他说过什么——下次他再出现，旧记忆照样注进提示词。
		// 置为 forgotten 而不是删行，出处链保留，和清空接口一致。
		groupPrefix, privatePrefix := memoryProfileSessionPrefixes(profileID)
		if _, err = tx.ExecContext(ctx, `
UPDATE memory_items
SET status = 'forgotten', updated_at = ?
WHERE status = 'active'
  AND subject_user_id = ?
  AND (substr(source_session, 1, length(?)) = ? OR substr(source_session, 1, length(?)) = ?)
`, time.Now().UTC().Unix(), userID, groupPrefix, groupPrefix, privatePrefix, privatePrefix); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM user_profiles WHERE bot_profile_id = ? AND user_id = ?", profileID, userID); err != nil {
			return err
		}
	} else {
		memories, err := json.Marshal(edit.Memories)
		if err != nil {
			return err
		}
		portrait, err := marshalUserPortrait(edit.Portrait)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `UPDATE user_profiles SET display_name = ?, favorability = ?, memories = ?, portrait = ?, updated_at = ? WHERE bot_profile_id = ? AND user_id = ?`, strings.TrimSpace(edit.DisplayName), edit.Favorability, string(memories), portrait, now, profileID, userID); err != nil {
			return err
		}
		if before != edit.Favorability {
			if _, err = tx.ExecContext(ctx, `INSERT INTO user_favorability_changes (bot_profile_id, user_id, delta, before_score, after_score, source, reason, created_at) VALUES (?, ?, ?, ?, ?, 'manual', 'WebUI 手动调整', ?)`, profileID, userID, edit.Favorability-before, before, edit.Favorability, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
