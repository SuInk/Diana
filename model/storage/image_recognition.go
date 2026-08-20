// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// 识别结果按图片内容哈希缓存，表情包和转发图在群里反复出现时只识别一次。
// 保留条数有上限：表情包是无限量的，不能让缓存跟着聊天量一直涨。
const maximumImageRecognitionRows = 20000

func (s *SQLiteStore) LoadImageRecognition(ctx context.Context, cacheKey string) (assistant.ImageRecognitionRecord, bool, error) {
	if s == nil || s.db == nil {
		return assistant.ImageRecognitionRecord{}, false, nil
	}
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return assistant.ImageRecognitionRecord{}, false, nil
	}
	var record assistant.ImageRecognitionRecord
	err := s.db.QueryRowContext(ctx, `
SELECT cache_key, content_sha256, kind, backend, COALESCE(model,''), text, created_at
FROM image_recognitions
WHERE cache_key = ?
`, cacheKey).Scan(
		&record.CacheKey,
		&record.ContentSHA256,
		&record.Kind,
		&record.Backend,
		&record.Model,
		&record.Text,
		&record.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.ImageRecognitionRecord{}, false, nil
	}
	if err != nil {
		return assistant.ImageRecognitionRecord{}, false, fmt.Errorf("load image recognition: %w", err)
	}
	return record, true, nil
}

func (s *SQLiteStore) SaveImageRecognition(ctx context.Context, record assistant.ImageRecognitionRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	record.CacheKey = strings.TrimSpace(record.CacheKey)
	record.ContentSHA256 = strings.ToLower(strings.TrimSpace(record.ContentSHA256))
	record.Text = strings.TrimSpace(record.Text)
	if record.CacheKey == "" || record.ContentSHA256 == "" || record.Text == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO image_recognitions (cache_key, content_sha256, kind, backend, model, text, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(cache_key) DO UPDATE SET
  text=excluded.text,
  created_at=excluded.created_at
`,
		record.CacheKey,
		record.ContentSHA256,
		strings.TrimSpace(record.Kind),
		strings.TrimSpace(record.Backend),
		strings.TrimSpace(record.Model),
		record.Text,
		record.CreatedAt,
	); err != nil {
		return fmt.Errorf("save image recognition: %w", err)
	}
	// 超出上限时按写入时间淘汰最旧的一批。
	if _, err := s.db.ExecContext(ctx, `
DELETE FROM image_recognitions
WHERE cache_key IN (
  SELECT cache_key FROM image_recognitions ORDER BY created_at DESC, cache_key DESC LIMIT -1 OFFSET ?
)
`, maximumImageRecognitionRows); err != nil {
		return fmt.Errorf("prune image recognitions: %w", err)
	}
	return nil
}
