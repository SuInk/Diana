// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// addUserProfileAIJudgmentColumn 给人员档案补上「像不像 AI」那一栏。补列即可，
// 不回填：判断只能从之后的记忆门控里攒。
func (s *SQLiteStore) addUserProfileAIJudgmentColumn() error {
	has, err := s.hasColumn("user_profiles", "ai_judgment")
	if err != nil || has {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE user_profiles ADD COLUMN ai_judgment TEXT NOT NULL DEFAULT ''`)
	return err
}

func unmarshalUserAIJudgment(raw sql.NullString) *assistant.UserAIJudgment {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var judgment assistant.UserAIJudgment
	// 坏掉的一栏当作没判断过：它只影响学说话时参考谁，不值得让整份档案读不出来。
	if err := json.Unmarshal([]byte(raw.String), &judgment); err != nil {
		return nil
	}
	judgment.Likely = judgment.LikelyAI()
	return &judgment
}

// updateUserAIJudgment 读出一个人的 AI 判断，交给 change 改，再写回。档案不存在时
// 新建一行：群里的机器人可能从没和 Diana 说过话，档案里还没有它。
func (s *SQLiteStore) updateUserAIJudgment(ctx context.Context, operation, botProfileID, userID, displayName string, change func(assistant.UserAIJudgment) assistant.UserAIJudgment) (assistant.UserAIJudgment, error) {
	defer s.observeStorage(ctx, operation, "write")()
	if s == nil || s.db == nil {
		return assistant.UserAIJudgment{}, nil
	}
	botProfileID, userID = strings.TrimSpace(botProfileID), strings.TrimSpace(userID)
	if userID == "" {
		return assistant.UserAIJudgment{}, nil
	}
	// 和档案的其余写入共用一把锁，免得和 UpdateUserMemory 的读改写交错。
	s.userMemoryMu.Lock()
	defer s.userMemoryMu.Unlock()
	tx, err := s.beginWriteTx(ctx, operation)
	if err != nil {
		return assistant.UserAIJudgment{}, err
	}
	defer observeTransaction(operation)()
	defer func() { _ = tx.Rollback() }()
	var raw sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT ai_judgment FROM user_profiles WHERE bot_profile_id = ? AND user_id = ?`, botProfileID, userID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return assistant.UserAIJudgment{}, err
	}
	var current assistant.UserAIJudgment
	if existing := unmarshalUserAIJudgment(raw); existing != nil {
		current = *existing
	}
	next := change(current)
	next.Likely = next.LikelyAI()
	body, err := json.Marshal(next)
	if err != nil {
		return assistant.UserAIJudgment{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
INSERT INTO user_profiles (bot_profile_id, user_id, display_name, favorability, message_count, memories, ai_judgment, updated_at)
VALUES (?, ?, ?, 0, 0, '[]', ?, ?)
ON CONFLICT(bot_profile_id, user_id) DO UPDATE SET ai_judgment = excluded.ai_judgment
`, botProfileID, userID, strings.TrimSpace(displayName), string(body), now)
	if err != nil {
		return assistant.UserAIJudgment{}, err
	}
	return next, tx.Commit()
}

func (s *SQLiteStore) ObserveUserAI(ctx context.Context, botProfileID, userID, displayName string, likelihood float64, reason string) (assistant.UserAIJudgment, error) {
	now := time.Now()
	return s.updateUserAIJudgment(ctx, "ObserveUserAI", botProfileID, userID, displayName, func(current assistant.UserAIJudgment) assistant.UserAIJudgment {
		return current.Observe(likelihood, reason, now)
	})
}

func (s *SQLiteStore) SetUserAIOverride(ctx context.Context, botProfileID, userID string, override assistant.UserAIOverride) (assistant.UserAIJudgment, error) {
	return s.updateUserAIJudgment(ctx, "SetUserAIOverride", botProfileID, userID, "", func(current assistant.UserAIJudgment) assistant.UserAIJudgment {
		current.Override = override
		current.UpdatedAt = time.Now().UTC()
		return current
	})
}

func (s *SQLiteStore) ListLikelyAIUsers(ctx context.Context) ([]assistant.UserAIRef, error) {
	defer s.observeStorage(ctx, "ListLikelyAIUsers", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.eventReader().QueryContext(ctx, `SELECT bot_profile_id, user_id, ai_judgment FROM user_profiles WHERE ai_judgment <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []assistant.UserAIRef
	for rows.Next() {
		var ref assistant.UserAIRef
		var raw sql.NullString
		if err := rows.Scan(&ref.BotProfileID, &ref.UserID, &raw); err != nil {
			return nil, err
		}
		if judgment := unmarshalUserAIJudgment(raw); judgment != nil && judgment.LikelyAI() {
			refs = append(refs, ref)
		}
	}
	return refs, rows.Err()
}
