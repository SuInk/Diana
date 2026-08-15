package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) LoadSemanticReferenceCache(ctx context.Context, cacheKey string) (assistant.SemanticReferenceCacheRecord, bool, error) {
	var record assistant.SemanticReferenceCacheRecord
	var messageIDs string
	err := s.db.QueryRowContext(ctx, `SELECT cache_key, message_ids, confidence, expires_at, created_at FROM semantic_reference_cache WHERE cache_key = ?`, strings.TrimSpace(cacheKey)).Scan(&record.CacheKey, &messageIDs, &record.Confidence, &record.ExpiresAt, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.SemanticReferenceCacheRecord{}, false, nil
	}
	if err != nil {
		return assistant.SemanticReferenceCacheRecord{}, false, fmt.Errorf("load semantic reference cache: %w", err)
	}
	if err := json.Unmarshal([]byte(messageIDs), &record.MessageIDs); err != nil {
		return assistant.SemanticReferenceCacheRecord{}, false, fmt.Errorf("decode semantic reference cache: %w", err)
	}
	return record, true, nil
}

func (s *SQLiteStore) SaveSemanticReferenceCache(ctx context.Context, record assistant.SemanticReferenceCacheRecord) error {
	messageIDs, err := json.Marshal(record.MessageIDs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO semantic_reference_cache (cache_key, message_ids, confidence, expires_at, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(cache_key) DO UPDATE SET message_ids=excluded.message_ids, confidence=excluded.confidence, expires_at=excluded.expires_at, created_at=excluded.created_at
`, strings.TrimSpace(record.CacheKey), string(messageIDs), record.Confidence, record.ExpiresAt, record.CreatedAt)
	if err != nil {
		return fmt.Errorf("save semantic reference cache: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM semantic_reference_cache WHERE expires_at <= ?`, time.Now().Unix())
	return nil
}
