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

const stickerAssetsBackfillKey = "sticker_assets_backfill_v1"

func (s *SQLiteStore) ensureStickerAssets() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sticker_assets (
  session TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  profile_id TEXT,
  context_namespace TEXT,
  kind TEXT NOT NULL,
  group_id TEXT,
  user_id TEXT,
  message_id TEXT,
  event_time INTEGER NOT NULL,
  segment_index INTEGER NOT NULL,
  summary TEXT,
  cached_file TEXT NOT NULL,
  cached_mime TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (session, content_sha256)
);
CREATE INDEX IF NOT EXISTS idx_sticker_assets_session_time
  ON sticker_assets(session, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_sticker_assets_namespace_kind_time
  ON sticker_assets(context_namespace, kind, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_sticker_assets_profile_kind_time
  ON sticker_assets(profile_id, kind, event_time DESC);
`); err != nil {
		return fmt.Errorf("create sticker asset index: %w", err)
	}

	var marker string
	err := s.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, stickerAssetsBackfillKey).Scan(&marker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read sticker asset backfill marker: %w", err)
	}
	if err := s.backfillStickerAssets(); err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO app_state (key, value, updated_at) VALUES (?, 'true', CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value='true', updated_at=CURRENT_TIMESTAMP
`, stickerAssetsBackfillKey)
	return err
}

func (s *SQLiteStore) backfillStickerAssets() error {
	_, err := s.db.Exec(`
INSERT INTO sticker_assets (
  session, content_sha256, profile_id, context_namespace, kind, group_id, user_id,
  message_id, event_time, segment_index, summary, cached_file, cached_mime, updated_at
)
SELECT
  event.session,
  LOWER(TRIM(json_extract(segment.value, '$.data.content_sha256'))),
  COALESCE(event.profile_id, TRIM(json_extract(event.payload, '$.profile_id')), ''),
  TRIM(COALESCE(json_extract(event.payload, '$.context_namespace'), '')),
  event.kind,
  COALESCE(event.group_id, ''),
  COALESCE(event.user_id, ''),
  COALESCE(event.message_id, ''),
  event.event_time,
  CAST(segment.key AS INTEGER),
  TRIM(COALESCE(json_extract(segment.value, '$.data.summary'), '')),
  TRIM(json_extract(segment.value, '$.data.cached_file')),
  TRIM(COALESCE(json_extract(segment.value, '$.data.cached_mime'), '')),
  CURRENT_TIMESTAMP
FROM message_events AS event, json_each(event.payload, '$.segments') AS segment
WHERE json_extract(segment.value, '$.type') = 'image'
  AND LENGTH(TRIM(json_extract(segment.value, '$.data.content_sha256'))) = 64
  AND LOWER(TRIM(json_extract(segment.value, '$.data.content_sha256'))) NOT GLOB '*[^0-9a-f]*'
  AND TRIM(COALESCE(json_extract(segment.value, '$.data.cached_file'), '')) != ''
  AND (
    TRIM(COALESCE(json_extract(segment.value, '$.data.summary'), ''), '[] ') NOT IN ('', '图片')
    OR TRIM(COALESCE(json_extract(segment.value, '$.data.sub_type'), '')) NOT IN ('', '0')
    OR TRIM(COALESCE(json_extract(segment.value, '$.data.emoji_id'), '')) != ''
    OR TRIM(COALESCE(json_extract(segment.value, '$.data.emoji_package_id'), '')) != ''
    OR TRIM(COALESCE(json_extract(segment.value, '$.data.emoji_type'), '')) != ''
  )
ON CONFLICT(session, content_sha256) DO UPDATE SET
  profile_id=excluded.profile_id,
  context_namespace=excluded.context_namespace,
  kind=excluded.kind,
  group_id=excluded.group_id,
  user_id=excluded.user_id,
  message_id=excluded.message_id,
  event_time=excluded.event_time,
  segment_index=excluded.segment_index,
  summary=excluded.summary,
  cached_file=excluded.cached_file,
  cached_mime=excluded.cached_mime,
  updated_at=excluded.updated_at
WHERE excluded.event_time >= sticker_assets.event_time
`)
	if err != nil {
		return fmt.Errorf("backfill sticker assets: %w", err)
	}
	return nil
}

func (s *SQLiteStore) indexStickerAssets(ctx context.Context, session string, event assistant.MessageEvent) error {
	for index, segment := range event.Segments {
		summary, ok := assistant.StickerSegmentLabel(segment)
		if !ok {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(segment.Data["content_sha256"]))
		path := strings.TrimSpace(segment.Data["cached_file"])
		if !validStickerAssetHash(hash) || path == "" {
			continue
		}
		eventTime := event.Time
		if eventTime <= 0 {
			eventTime = time.Now().Unix()
		}
		_, err := s.db.ExecContext(ctx, `
INSERT INTO sticker_assets (
  session, content_sha256, profile_id, context_namespace, kind, group_id, user_id,
  message_id, event_time, segment_index, summary, cached_file, cached_mime, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session, content_sha256) DO UPDATE SET
  profile_id=excluded.profile_id,
  context_namespace=excluded.context_namespace,
  kind=excluded.kind,
  group_id=excluded.group_id,
  user_id=excluded.user_id,
  message_id=excluded.message_id,
  event_time=excluded.event_time,
  segment_index=excluded.segment_index,
  summary=excluded.summary,
  cached_file=excluded.cached_file,
  cached_mime=excluded.cached_mime,
  updated_at=excluded.updated_at
WHERE excluded.event_time >= sticker_assets.event_time
`, session, hash, event.ProfileID, event.ContextNamespace, string(event.Kind), event.GroupID,
			event.UserID, event.MessageID, eventTime, index, summary, path,
			strings.TrimSpace(segment.Data["cached_mime"]), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	return nil
}

func validStickerAssetHash(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) < 0
}

// ListStickerAssets reserves a full limit for the current conversation and a
// separate limit for explicitly enabled shared scopes. Busy shared chats can no
// longer evict the current conversation's library before filtering.
func (s *SQLiteStore) ListStickerAssets(ctx context.Context, query assistant.StickerHistoryQuery) ([]assistant.StickerAsset, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	query.Session = strings.TrimSpace(query.Session)
	query.ContextNamespace = strings.TrimSpace(query.ContextNamespace)
	query.ProfileID = strings.TrimSpace(query.ProfileID)
	if query.Session == "" {
		return nil, nil
	}
	limit := normalizeMessageHistoryLimit(query.Limit)
	current, err := s.queryStickerAssets(ctx, "session = ?", []any{query.Session}, limit)
	if err != nil || (!query.ShareGroups && !query.SharePrivate) {
		return current, err
	}

	boundary := ""
	args := make([]any, 0, 4)
	switch {
	case query.ContextNamespace != "":
		boundary = "context_namespace = ?"
		args = append(args, query.ContextNamespace)
	case query.ProfileID != "":
		boundary = "profile_id = ?"
		args = append(args, query.ProfileID)
	default:
		return current, nil
	}
	args = append(args, query.Session)
	scopes := make([]string, 0, 2)
	if query.ShareGroups {
		scopes = append(scopes, "kind = ?")
		args = append(args, string(assistant.EventKindGroup))
	}
	if query.SharePrivate {
		scopes = append(scopes, "kind = ?")
		args = append(args, string(assistant.EventKindPrivate))
	}
	where := boundary + " AND session != ? AND (" + strings.Join(scopes, " OR ") + ")"
	shared, err := s.queryStickerAssets(ctx, where, args, limit)
	if err != nil {
		return nil, err
	}
	return append(current, shared...), nil
}

func (s *SQLiteStore) queryStickerAssets(ctx context.Context, where string, args []any, limit int) ([]assistant.StickerAsset, error) {
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT session, COALESCE(profile_id, ''), COALESCE(context_namespace, ''), kind,
       COALESCE(group_id, ''), COALESCE(user_id, ''), COALESCE(message_id, ''),
       event_time, segment_index, COALESCE(summary, ''), cached_file,
       COALESCE(cached_mime, ''), content_sha256
FROM sticker_assets
WHERE `+where+`
ORDER BY event_time DESC, updated_at DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	assets := make([]assistant.StickerAsset, 0, limit)
	for rows.Next() {
		var asset assistant.StickerAsset
		var kind string
		if err := rows.Scan(&asset.Session, &asset.ProfileID, &asset.ContextNamespace, &kind,
			&asset.GroupID, &asset.UserID, &asset.MessageID, &asset.EventTime,
			&asset.SegmentIndex, &asset.Summary, &asset.Path, &asset.MIME, &asset.ContentSHA256); err != nil {
			return nil, err
		}
		asset.Kind = assistant.EventKind(kind)
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}
