// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) SaveRepositoryIssueDraft(ctx context.Context, draft assistant.RepositoryIssueDraft) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("storage: repository issue draft store unavailable")
	}
	body, err := json.Marshal(draft)
	if err != nil {
		return fmt.Errorf("encode repository issue draft: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO repository_issue_drafts(id, group_id, status, payload, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  group_id = excluded.group_id,
  status = excluded.status,
  payload = excluded.payload,
  updated_at = excluded.updated_at
`, draft.ID, draft.GroupID, draft.Status, string(body), draft.CreatedAt.UTC().UnixNano(), draft.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("save repository issue draft: %w", err)
	}
	return nil
}

func (s *SQLiteStore) RepositoryIssueDraft(ctx context.Context, id string) (assistant.RepositoryIssueDraft, bool, error) {
	if s == nil || s.db == nil {
		return assistant.RepositoryIssueDraft{}, false, fmt.Errorf("storage: repository issue draft store unavailable")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM repository_issue_drafts WHERE id = ?`, strings.TrimSpace(id)).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return assistant.RepositoryIssueDraft{}, false, nil
		}
		return assistant.RepositoryIssueDraft{}, false, fmt.Errorf("load repository issue draft: %w", err)
	}
	var draft assistant.RepositoryIssueDraft
	if err := json.Unmarshal([]byte(payload), &draft); err != nil {
		return assistant.RepositoryIssueDraft{}, false, fmt.Errorf("decode repository issue draft: %w", err)
	}
	return draft, true, nil
}

func (s *SQLiteStore) ListRepositoryIssueDrafts(ctx context.Context, groupID, status string) ([]assistant.RepositoryIssueDraft, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("storage: repository issue draft store unavailable")
	}
	query := `SELECT payload FROM repository_issue_drafts WHERE 1 = 1`
	args := make([]any, 0, 2)
	if groupID = strings.TrimSpace(groupID); groupID != "" {
		query += ` AND group_id = ?`
		args = append(args, groupID)
	}
	if status = strings.TrimSpace(status); status != "" && status != "all" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list repository issue drafts: %w", err)
	}
	defer rows.Close()
	items := []assistant.RepositoryIssueDraft{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan repository issue draft: %w", err)
		}
		var draft assistant.RepositoryIssueDraft
		if err := json.Unmarshal([]byte(payload), &draft); err != nil {
			return nil, fmt.Errorf("decode repository issue draft: %w", err)
		}
		items = append(items, draft)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository issue drafts: %w", err)
	}
	return items, nil
}
