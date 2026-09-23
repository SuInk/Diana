// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// relationship_evaluations 记每一次后台好感度评估，不只是分数真的变了的那些。
// user_favorability_changes 只有「变了」，「为什么这句话没加分」——判了 0、把握
// 不够、调用失败、排满跳过——在那里一条都查不到。
//
// 每条回复都会评一次，这张表涨得和事件记录一样快，所以由存储维护按天清理。
const relationshipEvaluationsSchema = `
CREATE TABLE IF NOT EXISTS relationship_evaluations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bot_profile_id TEXT NOT NULL DEFAULT '',
  user_id TEXT NOT NULL,
  sender_name TEXT NOT NULL DEFAULT '',
  group_id TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  message_text TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  proposed_delta INTEGER NOT NULL DEFAULT 0,
  applied_delta INTEGER NOT NULL DEFAULT 0,
  before_score INTEGER NOT NULL DEFAULT 0,
  after_score INTEGER NOT NULL DEFAULT 0,
  confidence REAL NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  portrait TEXT NOT NULL DEFAULT '[]',
  portrait_count INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_relationship_evaluations_scope ON relationship_evaluations(bot_profile_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_relationship_evaluations_user ON relationship_evaluations(user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_relationship_evaluations_created ON relationship_evaluations(created_at);
`

func (s *SQLiteStore) migrateRelationshipEvaluations() error {
	if _, err := s.db.Exec(relationshipEvaluationsSchema); err != nil {
		return fmt.Errorf("create relationship evaluations schema: %w", err)
	}
	return nil
}

// RecordRelationshipEvaluation 记下一次后台好感度评估。
func (s *SQLiteStore) RecordRelationshipEvaluation(ctx context.Context, record assistant.RelationshipEvaluationRecord) error {
	defer s.observeStorage(ctx, "RecordRelationshipEvaluation", "write")()
	if s == nil || s.db == nil {
		return nil
	}
	if strings.TrimSpace(record.UserID) == "" || strings.TrimSpace(record.Status) == "" {
		return fmt.Errorf("好感度评估记录缺少用户或结果")
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	portrait := record.Portrait
	if portrait == nil {
		portrait = []assistant.RelationshipEvaluationPortrait{}
	}
	portraitJSON, err := json.Marshal(portrait)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO relationship_evaluations (
  bot_profile_id, user_id, sender_name, group_id, message_id, message_text, status,
  proposed_delta, applied_delta, before_score, after_score, confidence, reason, model, error,
  portrait, portrait_count, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(record.BotProfileID), strings.TrimSpace(record.UserID), record.SenderName, record.GroupID,
		record.MessageID, record.MessageText, record.Status, record.ProposedDelta, record.AppliedDelta,
		record.BeforeScore, record.AfterScore, record.Confidence, record.Reason, record.Model, record.Error,
		string(portraitJSON), len(portrait), createdAt.UTC().UnixNano())
	return err
}

// ListRelationshipEvaluations 按时间倒序列出评估记录，BeforeID 往前翻页。
func (s *SQLiteStore) ListRelationshipEvaluations(ctx context.Context, filter assistant.RelationshipEvaluationFilter) ([]assistant.RelationshipEvaluationRecord, error) {
	defer s.observeStorage(ctx, "ListRelationshipEvaluations", "read")()
	if s == nil || s.db == nil {
		return []assistant.RelationshipEvaluationRecord{}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var conditions []string
	var args []any
	if profile := strings.TrimSpace(filter.BotProfileID); profile != "" {
		conditions = append(conditions, "bot_profile_id = ?")
		args = append(args, profile)
	}
	if userID := strings.TrimSpace(filter.UserID); userID != "" {
		conditions = append(conditions, "user_id = ?")
		args = append(args, userID)
	}
	if groupID := strings.TrimSpace(filter.GroupID); groupID != "" {
		conditions = append(conditions, "group_id = ?")
		args = append(args, groupID)
	}
	if person := strings.TrimSpace(filter.Person); person != "" {
		pattern := "%" + escapeSQLiteLike(person) + "%"
		conditions = append(conditions, `(user_id LIKE ? ESCAPE '\' OR sender_name LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	// 搜索框什么都搜：页面上看得到的每一段文字都该能搜到，搜不到会让人以为没这条。
	// 画像存的是 JSON，中文不转义，直接对整段 LIKE 就能命中栏目名和内容。
	if query := strings.TrimSpace(filter.Query); query != "" {
		pattern := "%" + escapeSQLiteLike(query) + "%"
		columns := []string{"user_id", "sender_name", "group_id", "message_text", "reason", "portrait", "model", "error"}
		parts := make([]string, 0, len(columns))
		for _, column := range columns {
			parts = append(parts, column+` LIKE ? ESCAPE '\'`)
			args = append(args, pattern)
		}
		conditions = append(conditions, "("+strings.Join(parts, " OR ")+")")
	}
	if !filter.Since.IsZero() {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, filter.Since.UTC().UnixNano())
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, 0, len(filter.Statuses))
		for _, status := range filter.Statuses {
			placeholders = append(placeholders, "?")
			args = append(args, strings.TrimSpace(status))
		}
		conditions = append(conditions, "status IN ("+strings.Join(placeholders, ", ")+")")
	}
	if filter.HasPortrait {
		conditions = append(conditions, "portrait_count > 0")
	}
	if filter.BeforeID > 0 {
		conditions = append(conditions, "id < ?")
		args = append(args, filter.BeforeID)
	}
	query := `
SELECT id, bot_profile_id, user_id, sender_name, group_id, message_id, message_text, status,
       proposed_delta, applied_delta, before_score, after_score, confidence, reason, model, error, portrait, created_at
FROM relationship_evaluations`
	if len(conditions) > 0 {
		query += "\nWHERE " + strings.Join(conditions, " AND ")
	}
	query += "\nORDER BY id DESC\nLIMIT ?"
	args = append(args, limit)
	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]assistant.RelationshipEvaluationRecord, 0, limit)
	for rows.Next() {
		var record assistant.RelationshipEvaluationRecord
		var createdAt int64
		var portrait string
		if err := rows.Scan(&record.ID, &record.BotProfileID, &record.UserID, &record.SenderName, &record.GroupID,
			&record.MessageID, &record.MessageText, &record.Status, &record.ProposedDelta, &record.AppliedDelta,
			&record.BeforeScore, &record.AfterScore, &record.Confidence, &record.Reason, &record.Model, &record.Error,
			&portrait, &createdAt); err != nil {
			return nil, err
		}
		if portrait != "" && portrait != "[]" {
			if err := json.Unmarshal([]byte(portrait), &record.Portrait); err != nil {
				return nil, fmt.Errorf("decode relationship evaluation portrait: %w", err)
			}
		}
		record.CreatedAt = time.Unix(0, createdAt).UTC()
		records = append(records, record)
	}
	return records, rows.Err()
}

// PruneRelationshipEvaluations 删掉早于 createdBefore 的评估记录，分批删免得长时间占写锁。
func (s *SQLiteStore) PruneRelationshipEvaluations(ctx context.Context, createdBefore time.Time) (int64, error) {
	if s == nil || s.db == nil || createdBefore.IsZero() {
		return 0, nil
	}
	cutoff := createdBefore.UTC().UnixNano()
	var deleted int64
	for {
		result, err := s.db.ExecContext(ctx, `DELETE FROM relationship_evaluations WHERE id IN (
SELECT id FROM relationship_evaluations WHERE created_at < ? ORDER BY created_at LIMIT 500)`, cutoff)
		if err != nil {
			return deleted, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += count
		if count < 500 {
			return deleted, nil
		}
	}
}
