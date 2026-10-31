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

// StickerTransferItem 是导出导入时的一张表情包：图片本身之外，把标注一起带上，
// 导入的一端不用再花识图调用重新标。Path 只在服务端用，不进导出的清单。
type StickerTransferItem struct {
	Hash        string   `json:"hash"`
	Summary     string   `json:"summary,omitempty"`
	MIME        string   `json:"mime,omitempty"`
	Description string   `json:"description,omitempty"`
	Gist        string   `json:"gist,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Category    string   `json:"category,omitempty"`
	// TagVersion 是标注版本，GIF 只认当前版本的标注，版本带过去才不会被当成没标过。
	TagVersion string `json:"tag_version,omitempty"`
	// Tagged 表示导出端标注过（标签可以为空），导入时据此决定要不要写标注。
	Tagged bool   `json:"tagged,omitempty"`
	Path   string `json:"-"`
}

// ListStickerTransferItems 按控制台的筛选条件列出要导出的全部表情包，不分页。
// 同一张图取最近那条记录的文件。
func (s *SQLiteStore) ListStickerTransferItems(ctx context.Context, query StickerLibraryQuery) ([]StickerTransferItem, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	base, args := stickerLibraryCTE(query, time.Now())
	rows, err := s.eventReader().QueryContext(ctx, base+`
SELECT m.content_sha256, COALESCE(a.summary, ''), COALESCE(a.cached_mime, ''), a.cached_file,
       COALESCE(d.description, ''), t.content_sha256 IS NOT NULL, COALESCE(t.gist, ''), COALESCE(t.tags, ''),
       COALESCE(t.category, ''), COALESCE(t.version, '')
FROM matched AS m
JOIN scoped AS latest ON latest.content_sha256 = m.content_sha256 AND latest.rank = 1
JOIN sticker_assets AS a ON a.rowid = latest.row_id
LEFT JOIN image_descriptions AS d ON d.content_sha256 = m.content_sha256
LEFT JOIN sticker_tags AS t ON t.content_sha256 = m.content_sha256
ORDER BY m.last_seen DESC, m.content_sha256`, args...)
	if err != nil {
		return nil, fmt.Errorf("list sticker export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []StickerTransferItem
	for rows.Next() {
		var item StickerTransferItem
		var tags string
		if err := rows.Scan(&item.Hash, &item.Summary, &item.MIME, &item.Path, &item.Description,
			&item.Tagged, &item.Gist, &tags, &item.Category, &item.TagVersion); err != nil {
			return nil, fmt.Errorf("scan sticker export: %w", err)
		}
		item.Tags = decodeStickerTags(tags)
		items = append(items, item)
	}
	return items, rows.Err()
}

// ImportStickerAsset 把一张导入的表情包收进 profileID 的池子，挂在导入用的虚拟会话下，
// 这台机器人在所有会话里都能挑到。主人主动导入，所以顺带解除这台机器人对这张图的拉黑。
// 本地已有的标注和图片描述优先，导入的只补空缺。返回 false 表示这台机器人的导入里已经有这张。
func (s *SQLiteStore) ImportStickerAsset(ctx context.Context, profileID, path string, item StickerTransferItem) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	profileID = strings.TrimSpace(profileID)
	hash := strings.ToLower(strings.TrimSpace(item.Hash))
	if profileID == "" || !validStickerAssetHash(hash) || strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("导入表情包缺少机器人、哈希或文件")
	}
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		summary = "动画表情"
	}
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
INSERT INTO sticker_assets (
  session, content_sha256, profile_id, context_namespace, kind, group_id, user_id,
  message_id, event_time, segment_index, summary, cached_file, cached_mime, updated_at
) VALUES (?, ?, ?, '', ?, '', '', '', ?, 0, ?, ?, ?, ?)
ON CONFLICT(session, content_sha256) DO NOTHING`,
		assistant.StickerImportSession(profileID), hash, profileID, string(assistant.StickerImportKind),
		now.Unix(), summary, path, strings.TrimSpace(item.MIME), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("import sticker asset: %w", err)
	}
	added, _ := result.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sticker_blocklist WHERE profile_id = ? AND content_sha256 = ?`, profileID, hash); err != nil {
		return false, fmt.Errorf("unblock imported sticker: %w", err)
	}
	if item.Tagged {
		tags := item.Tags
		if tags == nil {
			tags = []string{}
		}
		encoded, err := json.Marshal(tags)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO sticker_tags (content_sha256, gist, tags, category, version, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(content_sha256) DO NOTHING`, hash, strings.TrimSpace(item.Gist), string(encoded),
			assistant.NormalizeStickerCategory(item.Category), strings.TrimSpace(item.TagVersion), now.Unix()); err != nil {
			return false, fmt.Errorf("import sticker tags: %w", err)
		}
	}
	if description := strings.TrimSpace(item.Description); description != "" {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO image_descriptions (content_sha256, description, source, created_at, updated_at) VALUES (?, ?, 'sticker_import', ?, ?)
ON CONFLICT(content_sha256) DO NOTHING`, hash, description, now.Unix(), now.Unix()); err != nil {
			return false, fmt.Errorf("import sticker description: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return added > 0, nil
}
