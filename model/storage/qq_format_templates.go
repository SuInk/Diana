// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// QQ 官方机器人的格式模板按机器人档案各存一份整表 JSON，条数很少，不值得单开表。
const qqFormatTemplatesKeyPrefix = "qq_format_templates:"

func qqFormatTemplatesKey(profileID string) string {
	return qqFormatTemplatesKeyPrefix + strings.TrimSpace(profileID)
}

// LoadQQFormatTemplates 返回存过的模板表原文；没存过时返回 nil。
func (s *SQLiteStore) LoadQQFormatTemplates(ctx context.Context, profileID string) ([]byte, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, qqFormatTemplatesKey(profileID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

// SaveQQFormatTemplates 整表覆盖保存。
func (s *SQLiteStore) SaveQQFormatTemplates(ctx context.Context, profileID string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO app_state (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP
`, qqFormatTemplatesKey(profileID), string(data))
	return err
}
