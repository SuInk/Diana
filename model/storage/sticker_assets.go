// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

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
-- 表情包专用的检索标签。和 image_descriptions 分开：那份描述是给聊天上下文看的客观描述，
-- 这份是按「借这张图想说什么」写的，行存在即表示已经标注过（标签可以为空）。
CREATE TABLE IF NOT EXISTS sticker_tags (
  content_sha256 TEXT PRIMARY KEY,
  gist TEXT NOT NULL,
  tags TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
-- 这张表情包合不合某份人设：以机器人本人的身份发它自不自然。按人设全文的指纹存，人设改了就重判。
CREATE TABLE IF NOT EXISTS sticker_persona_fit (
  persona_key TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  fit INTEGER NOT NULL,
  reason TEXT,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (persona_key, content_sha256)
);
-- 控制台里删掉的表情包：之后再有人发同一张也不收。profile_id 为空表示对所有机器人生效。
CREATE TABLE IF NOT EXISTS sticker_blocklist (
  profile_id TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (profile_id, content_sha256)
);
-- 机器人在某个会话里发过哪张表情包：用来避免连发同一张，也算作这张图「还在用」。
CREATE TABLE IF NOT EXISTS sticker_usage (
  session TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  sent_count INTEGER NOT NULL,
  last_sent_at INTEGER NOT NULL,
  PRIMARY KEY (session, content_sha256)
);
`); err != nil {
		return fmt.Errorf("create sticker asset index: %w", err)
	}
	// 标签按哪种看图方式标的：动图改成多帧分镜之前只看了第一帧，那些 GIF 的标签要重标。
	if has, err := s.hasColumn("sticker_tags", "version"); err != nil {
		return err
	} else if !has {
		if _, err := s.db.Exec(`ALTER TABLE sticker_tags ADD COLUMN version TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add sticker tag version: %w", err)
		}
	}

	// 画风大类是后加的。NULL 表示这张还没判过大类，由检索时补标；判过但没判出来存空串。
	if has, err := s.hasColumn("sticker_tags", "category"); err != nil {
		return err
	} else if !has {
		if _, err := s.db.Exec(`ALTER TABLE sticker_tags ADD COLUMN category TEXT`); err != nil {
			return fmt.Errorf("add sticker tag category: %w", err)
		}
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
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM sticker_blocklist AS b WHERE b.content_sha256 = ? AND b.profile_id IN (?, '')
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
`, session, hash, event.ProfileID, event.ContextNamespace, string(event.Kind), event.GroupID,
			event.UserID, event.MessageID, eventTime, index, summary, path,
			strings.TrimSpace(segment.Data["cached_mime"]), time.Now().UTC().Format(time.RFC3339Nano),
			hash, event.ProfileID)
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
	sessions := stickerSessionKeys(query.Session)
	current, err := s.queryStickerAssets(ctx, query.Session, "a.session IN ("+sqlPlaceholders(len(sessions))+")", stringArgs(sessions), limit)
	for index := range current {
		current[index].Session = query.Session
	}
	if err != nil || (!query.ShareGroups && !query.SharePrivate) {
		return current, err
	}

	boundary := ""
	args := make([]any, 0, 4)
	switch {
	case query.ContextNamespace != "":
		boundary = "a.context_namespace = ?"
		args = append(args, query.ContextNamespace)
	case query.ProfileID != "":
		boundary = "a.profile_id = ?"
		args = append(args, query.ProfileID)
	default:
		return current, nil
	}
	args = append(args, stringArgs(sessions)...)
	scopes := make([]string, 0, 2)
	if query.ShareGroups {
		scopes = append(scopes, "a.kind = ?")
		args = append(args, string(assistant.EventKindGroup))
	}
	if query.SharePrivate {
		scopes = append(scopes, "a.kind = ?")
		args = append(args, string(assistant.EventKindPrivate))
	}
	where := boundary + " AND a.session NOT IN (" + sqlPlaceholders(len(sessions)) + ") AND (" + strings.Join(scopes, " OR ") + ")"
	shared, err := s.queryStickerAssets(ctx, query.Session, where, args, limit)
	if err != nil {
		return nil, err
	}
	return append(current, shared...), nil
}

// stickerTagCurrent 判断一行标签还算不算数：静态图的旧标签照用；GIF 以前只看了第一帧，
// 只认按当前标注版本（多帧分镜）标过的。不算数的当作没标注，由检索时补标。
const stickerTagCurrent = `(t.content_sha256 IS NOT NULL AND (COALESCE(t.version, '') = '` + assistant.StickerAnnotationVersion + `'
  OR (LOWER(COALESCE(a.cached_mime, '')) <> 'image/gif' AND LOWER(a.cached_file) NOT LIKE '%.gif')))`

// queryStickerAssets 顺带取出简介、标签和机器人在 currentSession 里的发送记录，
// 候选排序和防重复都靠这几列，不必再逐张回查。
func (s *SQLiteStore) queryStickerAssets(ctx context.Context, currentSession, where string, args []any, limit int) ([]assistant.StickerAsset, error) {
	args = append([]any{currentSession}, args...)
	args = append(args, limit)
	// 表情库浏览和检索都是只读的，走读池。
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT a.session, COALESCE(a.profile_id, ''), COALESCE(a.context_namespace, ''), a.kind,
       COALESCE(a.group_id, ''), COALESCE(a.user_id, ''), COALESCE(a.message_id, ''),
       a.event_time, a.segment_index, COALESCE(a.summary, ''), a.cached_file,
       COALESCE(a.cached_mime, ''), a.content_sha256,
       COALESCE(d.description, ''), `+stickerTagCurrent+`, CASE WHEN `+stickerTagCurrent+` THEN COALESCE(t.gist, '') ELSE '' END,
       CASE WHEN `+stickerTagCurrent+` THEN COALESCE(t.tags, '') ELSE '' END,
       COALESCE(t.category, ''), t.category IS NOT NULL,
       COALESCE(u.sent_count, 0), COALESCE(u.last_sent_at, 0)
FROM sticker_assets AS a
LEFT JOIN image_descriptions AS d ON d.content_sha256 = a.content_sha256
LEFT JOIN sticker_tags AS t ON t.content_sha256 = a.content_sha256
LEFT JOIN sticker_usage AS u ON u.session = ? AND u.content_sha256 = a.content_sha256
WHERE `+where+`
ORDER BY a.event_time DESC, a.updated_at DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	assets := make([]assistant.StickerAsset, 0, limit)
	for rows.Next() {
		var asset assistant.StickerAsset
		var kind, tags string
		if err := rows.Scan(&asset.Session, &asset.ProfileID, &asset.ContextNamespace, &kind,
			&asset.GroupID, &asset.UserID, &asset.MessageID, &asset.EventTime,
			&asset.SegmentIndex, &asset.Summary, &asset.Path, &asset.MIME, &asset.ContentSHA256,
			&asset.Description, &asset.Tagged, &asset.Gist, &tags, &asset.Category, &asset.CategoryKnown,
			&asset.SentCount, &asset.LastSentAt); err != nil {
			return nil, err
		}
		asset.Kind = assistant.EventKind(kind)
		asset.Tags = decodeStickerTags(tags)
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

// stickerSessionKeys 返回一个会话在表情包库里的全部键：当前带命名空间的键，加上引入
// 命名空间之前落库的旧键（只有 group:… / private:…）。旧键下的表情包属于同一个会话，
// 不带上它们，老群攒下的大半库存永远进不了候选。
func stickerSessionKeys(session string) []string {
	keys := []string{session}
	for _, marker := range []string{":group:", ":private:"} {
		if index := strings.LastIndex(session, marker); index > 0 {
			keys = append(keys, session[index+1:])
			break
		}
	}
	return keys
}

func sqlPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

func decodeStickerTags(raw string) []string {
	var tags []string
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &tags) != nil {
		return nil
	}
	return tags
}

// SaveStickerTags 记下一张表情包的检索标签；标签为空也落一行，表示标注过了，不再重复调识图。
func (s *SQLiteStore) SaveStickerTags(ctx context.Context, record assistant.StickerTagRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	hash := strings.ToLower(strings.TrimSpace(record.ContentSHA256))
	if !validStickerAssetHash(hash) {
		return nil
	}
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	encoded, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO sticker_tags (content_sha256, gist, tags, category, version, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(content_sha256) DO UPDATE SET gist=excluded.gist, tags=excluded.tags, category=excluded.category, version=excluded.version, updated_at=excluded.updated_at
`, hash, strings.TrimSpace(record.Gist), string(encoded), assistant.NormalizeStickerCategory(record.Category), strings.TrimSpace(record.Version), time.Now().Unix())
	return err
}

// RecordStickerSent 记一次机器人在 session 里发出这张表情包。
func (s *SQLiteStore) RecordStickerSent(ctx context.Context, session, contentSHA256 string, sentAt int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	session = strings.TrimSpace(session)
	hash := strings.ToLower(strings.TrimSpace(contentSHA256))
	if session == "" || !validStickerAssetHash(hash) {
		return nil
	}
	if sentAt <= 0 {
		sentAt = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sticker_usage (session, content_sha256, sent_count, last_sent_at) VALUES (?, ?, 1, ?)
ON CONFLICT(session, content_sha256) DO UPDATE SET
  sent_count=sticker_usage.sent_count + 1,
  last_sent_at=MAX(sticker_usage.last_sent_at, excluded.last_sent_at)
`, session, hash, sentAt)
	return err
}

// StickerPersonaFit 读出这些表情包在某份人设下的判断结果；没判过的不在结果里。
func (s *SQLiteStore) StickerPersonaFit(ctx context.Context, personaKey string, hashes []string) (map[string]bool, error) {
	result := map[string]bool{}
	personaKey = strings.TrimSpace(personaKey)
	if s == nil || s.db == nil || personaKey == "" || len(hashes) == 0 {
		return result, nil
	}
	for start := 0; start < len(hashes); start += 500 {
		batch := hashes[start:min(start+500, len(hashes))]
		rows, err := s.eventReader().QueryContext(ctx, `SELECT content_sha256, fit FROM sticker_persona_fit WHERE persona_key = ? AND content_sha256 IN (`+sqlPlaceholders(len(batch))+`)`,
			append([]any{personaKey}, stringArgs(batch)...)...)
		if err != nil {
			return nil, fmt.Errorf("load sticker persona fit: %w", err)
		}
		for rows.Next() {
			var hash string
			var fit bool
			if err := rows.Scan(&hash, &fit); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result[hash] = fit
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// SaveStickerPersonaFit 记下一次人设判断。
func (s *SQLiteStore) SaveStickerPersonaFit(ctx context.Context, personaKey, contentSHA256 string, fit bool, reason string) error {
	if s == nil || s.db == nil {
		return nil
	}
	personaKey = strings.TrimSpace(personaKey)
	hash := strings.ToLower(strings.TrimSpace(contentSHA256))
	if personaKey == "" || !validStickerAssetHash(hash) {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sticker_persona_fit (persona_key, content_sha256, fit, reason, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(persona_key, content_sha256) DO UPDATE SET fit=excluded.fit, reason=excluded.reason, updated_at=excluded.updated_at
`, personaKey, hash, fit, strings.TrimSpace(reason), time.Now().Unix())
	return err
}

// PruneStickerAssets 把一个会话的表情包库压到 capacity 张以内。按「最后一次用到」淘汰：
// 有人发过或机器人发过都算用到，最久没用的先走。只删索引，图片文件归聊天记录管。
func (s *SQLiteStore) PruneStickerAssets(ctx context.Context, session string, capacity int) (int, error) {
	if s == nil || s.db == nil || capacity <= 0 {
		return 0, nil
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return 0, nil
	}
	// 每条带表情包的消息都会来问一次，没超上限时只读计数，不占写连接。
	sessions := stickerSessionKeys(session)
	in := "(" + sqlPlaceholders(len(sessions)) + ")"
	var total int
	if err := s.eventReader().QueryRowContext(ctx, `SELECT COUNT(DISTINCT content_sha256) FROM sticker_assets WHERE session IN `+in, stringArgs(sessions)...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count sticker assets: %w", err)
	}
	if total <= capacity {
		return 0, nil
	}
	// 新旧两种会话键下的同一张图按一张算，取最近一次用到的时间排序。
	args := append(stringArgs(sessions), session)
	args = append(args, stringArgs(sessions)...)
	args = append(args, capacity)
	result, err := s.db.ExecContext(ctx, `
DELETE FROM sticker_assets
WHERE session IN `+in+` AND content_sha256 IN (
  SELECT a.content_sha256
  FROM sticker_assets AS a
  LEFT JOIN sticker_usage AS u ON u.session = ? AND u.content_sha256 = a.content_sha256
  WHERE a.session IN `+in+`
  GROUP BY a.content_sha256
  ORDER BY MAX(MAX(a.event_time), COALESCE(MAX(u.last_sent_at), 0)) DESC, a.content_sha256
  LIMIT -1 OFFSET ?
)`, args...)
	if err != nil {
		return 0, fmt.Errorf("prune sticker assets: %w", err)
	}
	removed, _ := result.RowsAffected()
	return int(removed), nil
}

// StickerLibraryUncategorized 作为 StickerLibraryQuery.Category 时只列还没判出画风大类的。
const StickerLibraryUncategorized = "none"

// StickerLibraryQuery 是控制台浏览和清理表情包池的筛选条件，各条件同时生效。
// ProfileID 为空时覆盖全部机器人。
type StickerLibraryQuery struct {
	ProfileID string
	Search    string
	// Category 是画风大类，StickerLibraryUncategorized 表示没判出大类的。
	Category string
	// Tag 按关键词整词匹配。
	Tag string
	// Source 是来源会话：group:<群号> 或 private（全部私聊）。按来源清理时只移出这个来源的记录。
	Source string
	// IdleDays 只看最近这么多天既没人发、机器人也没发过的。
	IdleDays int
	// NeverSent 只看机器人从没发过的。
	NeverSent bool
	// Sort：recent（默认，最近出现在前）、idle（最久没用在前）、most_sent（机器人发得多的在前）。
	Sort   string
	Limit  int
	Offset int
}

// StickerLibraryItem 是池子里的一张表情包。同一张图在多个会话里出现只列一次，
// 字段取最近那次；Sessions 是它出现过的会话数。本地路径不在这里，取图走 StickerAssetFile。
type StickerLibraryItem struct {
	Hash        string     `json:"hash"`
	Summary     string     `json:"summary"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Category    string     `json:"category,omitempty"`
	MIME        string     `json:"mime,omitempty"`
	Kind        string     `json:"kind"`
	GroupID     string     `json:"group_id,omitempty"`
	UserID      string     `json:"user_id,omitempty"`
	ProfileID   string     `json:"profile_id,omitempty"`
	Sessions    int        `json:"sessions"`
	LastSeen    time.Time  `json:"last_seen"`
	SentCount   int        `json:"sent_count"`
	LastSent    *time.Time `json:"last_sent,omitempty"`
}

type StickerLibraryPage struct {
	Items []StickerLibraryItem `json:"items"`
	Total int                  `json:"total"`
}

// stickerLibraryCTE 把筛选条件拼成三段 CTE：scoped 是命中条件的资产行（每张图最近那行 rank=1），
// usage 是机器人按图片汇总的发送记录，matched 是按图片去重、再过一遍「多久没用」条件后的结果。
// 列表、分类计数和清理都从这里出发，数字才对得上。
func stickerLibraryCTE(query StickerLibraryQuery, now time.Time) (string, []any) {
	conditions := []string{"1 = 1"}
	args := []any{}
	if profile := strings.TrimSpace(query.ProfileID); profile != "" {
		conditions = append(conditions, "a.profile_id = ?")
		args = append(args, profile)
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		pattern := "%" + escapeSQLiteLike(search) + "%"
		conditions = append(conditions, `(a.summary LIKE ? ESCAPE '\' OR COALESCE(d.description, '') LIKE ? ESCAPE '\'
  OR COALESCE(t.gist, '') LIKE ? ESCAPE '\' OR COALESCE(t.tags, '') LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	switch category := strings.TrimSpace(query.Category); category {
	case "":
	case StickerLibraryUncategorized:
		conditions = append(conditions, `COALESCE(t.category, '') = ''`)
	default:
		conditions = append(conditions, `t.category = ?`)
		args = append(args, category)
	}
	if tag := strings.TrimSpace(query.Tag); tag != "" {
		conditions = append(conditions, `EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(t.tags) THEN t.tags ELSE '[]' END) WHERE value = ?)`)
		args = append(args, tag)
	}
	switch source := strings.TrimSpace(query.Source); {
	case source == "":
	case source == "private":
		conditions = append(conditions, `a.kind = ?`)
		args = append(args, string(assistant.EventKindPrivate))
	case strings.HasPrefix(source, "group:"):
		conditions = append(conditions, `a.kind = ? AND a.group_id = ?`)
		args = append(args, string(assistant.EventKindGroup), strings.TrimPrefix(source, "group:"))
	default:
		conditions = append(conditions, `0 = 1`)
	}
	having := []string{"1 = 1"}
	if query.IdleDays > 0 {
		cutoff := now.Add(-time.Duration(query.IdleDays) * 24 * time.Hour).Unix()
		having = append(having, `MAX(s.event_time) < ? AND COALESCE(MAX(u.last_sent_at), 0) < ?`)
		args = append(args, cutoff, cutoff)
	}
	if query.NeverSent {
		having = append(having, `COALESCE(MAX(u.sent_count), 0) = 0`)
	}
	return `
WITH scoped AS (
  SELECT a.rowid AS row_id, a.content_sha256, a.kind, COALESCE(a.group_id, '') AS group_id, a.event_time,
         ROW_NUMBER() OVER (PARTITION BY a.content_sha256 ORDER BY a.event_time DESC, a.updated_at DESC) AS rank
  FROM sticker_assets AS a
  LEFT JOIN image_descriptions AS d ON d.content_sha256 = a.content_sha256
  LEFT JOIN sticker_tags AS t ON t.content_sha256 = a.content_sha256
  WHERE ` + strings.Join(conditions, " AND ") + `
),
usage AS (
  SELECT content_sha256, SUM(sent_count) AS sent_count, MAX(last_sent_at) AS last_sent_at
  FROM sticker_usage GROUP BY content_sha256
),
matched AS (
  SELECT s.content_sha256, MAX(s.event_time) AS last_seen, COUNT(*) AS sessions,
         COALESCE(MAX(u.sent_count), 0) AS sent_count, COALESCE(MAX(u.last_sent_at), 0) AS last_sent_at
  FROM scoped AS s
  LEFT JOIN usage AS u ON u.content_sha256 = s.content_sha256
  GROUP BY s.content_sha256
  HAVING ` + strings.Join(having, " AND ") + `
)`, args
}

// ListStickerLibrary 列出已经收进池子、符合筛选条件的表情包。
func (s *SQLiteStore) ListStickerLibrary(ctx context.Context, query StickerLibraryQuery) (StickerLibraryPage, error) {
	page := StickerLibraryPage{Items: []StickerLibraryItem{}}
	if s == nil || s.db == nil {
		return page, nil
	}
	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	offset := max(query.Offset, 0)
	base, args := stickerLibraryCTE(query, time.Now())
	if err := s.eventReader().QueryRowContext(ctx, base+`SELECT COUNT(*) FROM matched`, args...).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("count sticker library: %w", err)
	}
	order := "m.last_seen DESC, m.content_sha256"
	switch query.Sort {
	case "idle":
		order = "MAX(m.last_seen, m.last_sent_at) ASC, m.content_sha256"
	case "most_sent":
		order = "m.sent_count DESC, m.last_seen DESC, m.content_sha256"
	}
	rows, err := s.eventReader().QueryContext(ctx, base+`
SELECT m.content_sha256, COALESCE(a.summary, ''), COALESCE(NULLIF(t.gist, ''), d.description, ''),
       COALESCE(t.tags, ''), COALESCE(t.category, ''), COALESCE(a.cached_mime, ''), a.kind,
       COALESCE(a.group_id, ''), COALESCE(a.user_id, ''), COALESCE(a.profile_id, ''),
       m.sessions, m.last_seen, m.sent_count, m.last_sent_at
FROM matched AS m
JOIN scoped AS latest ON latest.content_sha256 = m.content_sha256 AND latest.rank = 1
JOIN sticker_assets AS a ON a.rowid = latest.row_id
LEFT JOIN image_descriptions AS d ON d.content_sha256 = m.content_sha256
LEFT JOIN sticker_tags AS t ON t.content_sha256 = m.content_sha256
ORDER BY `+order+`
LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return page, fmt.Errorf("list sticker library: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item StickerLibraryItem
		var lastSeen, lastSent int64
		var tags string
		if err := rows.Scan(&item.Hash, &item.Summary, &item.Description, &tags, &item.Category, &item.MIME, &item.Kind,
			&item.GroupID, &item.UserID, &item.ProfileID, &item.Sessions, &lastSeen, &item.SentCount, &lastSent); err != nil {
			return page, fmt.Errorf("scan sticker library: %w", err)
		}
		item.Tags = decodeStickerTags(tags)
		item.LastSeen = time.Unix(lastSeen, 0)
		if lastSent > 0 {
			sent := time.Unix(lastSent, 0)
			item.LastSent = &sent
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

// StickerAssetFile 按图片哈希取缓存文件路径，供控制台预览。profileID 非空时只认这个
// 机器人收到过的，免得按机器人筛选的页面能拿到别的机器人的图。
func (s *SQLiteStore) StickerAssetFile(ctx context.Context, hash, profileID string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if !validStickerAssetHash(hash) {
		return "", false, nil
	}
	query := `SELECT cached_file FROM sticker_assets WHERE content_sha256 = ?`
	args := []any{hash}
	if profile := strings.TrimSpace(profileID); profile != "" {
		query += ` AND profile_id = ?`
		args = append(args, profile)
	}
	var path string
	err := s.eventReader().QueryRowContext(ctx, query+` ORDER BY event_time DESC LIMIT 1`, args...).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load sticker asset file: %w", err)
	}
	return path, true, nil
}

// StickerFacetCount 是分类栏上一个选项和它下面有几张图。
type StickerFacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// StickerLibraryFacets 是控制台的分类栏：画风大类、来源会话和常见关键词。
// 每一栏都按「其他条件不变、只放开这一栏」来数，点哪个选项看到的张数就是分类栏上写的。
type StickerLibraryFacets struct {
	// Categories 按 StickerCategories 的顺序，Value 为 StickerLibraryUncategorized 的是没判出大类的。
	Categories []StickerFacetCount `json:"categories"`
	// Sources 的 Value 是 group:<群号> 或 private，张数多的在前。
	Sources []StickerFacetCount `json:"sources"`
	// Tags 是出现最多的关键词。
	Tags []StickerFacetCount `json:"tags"`
}

// ListStickerLibraryFacets 统计分类栏，都按图片去重计数。
func (s *SQLiteStore) ListStickerLibraryFacets(ctx context.Context, query StickerLibraryQuery, tagLimit int) (StickerLibraryFacets, error) {
	facets := StickerLibraryFacets{Categories: []StickerFacetCount{}, Sources: []StickerFacetCount{}, Tags: []StickerFacetCount{}}
	if s == nil || s.db == nil {
		return facets, nil
	}
	if tagLimit <= 0 || tagLimit > 100 {
		tagLimit = 40
	}
	now := time.Now()

	withoutCategory := query
	withoutCategory.Category = ""
	base, args := stickerLibraryCTE(withoutCategory, now)
	counts, err := s.stickerFacetCounts(ctx, base+`
SELECT COALESCE(NULLIF(t.category, ''), '`+StickerLibraryUncategorized+`'), COUNT(*)
FROM matched AS m LEFT JOIN sticker_tags AS t ON t.content_sha256 = m.content_sha256
GROUP BY 1`, args)
	if err != nil {
		return facets, fmt.Errorf("count sticker categories: %w", err)
	}
	byValue := map[string]int{}
	for _, item := range counts {
		byValue[item.Value] = item.Count
	}
	for _, category := range append(append([]string{}, assistant.StickerCategories...), StickerLibraryUncategorized) {
		if byValue[category] > 0 {
			facets.Categories = append(facets.Categories, StickerFacetCount{Value: category, Count: byValue[category]})
		}
	}

	withoutSource := query
	withoutSource.Source = ""
	base, args = stickerLibraryCTE(withoutSource, now)
	if facets.Sources, err = s.stickerFacetCounts(ctx, base+`
SELECT CASE WHEN s.kind = '`+string(assistant.EventKindPrivate)+`' THEN 'private' ELSE 'group:' || s.group_id END AS source,
       COUNT(DISTINCT s.content_sha256) AS total
FROM scoped AS s JOIN matched AS m ON m.content_sha256 = s.content_sha256
GROUP BY source
ORDER BY total DESC, source`, args); err != nil {
		return facets, fmt.Errorf("count sticker sources: %w", err)
	}

	withoutTag := query
	withoutTag.Tag = ""
	base, args = stickerLibraryCTE(withoutTag, now)
	if facets.Tags, err = s.stickerFacetCounts(ctx, base+`
SELECT tag.value, COUNT(*) AS total
FROM matched AS m
JOIN sticker_tags AS t ON t.content_sha256 = m.content_sha256,
     json_each(CASE WHEN json_valid(t.tags) THEN t.tags ELSE '[]' END) AS tag
WHERE TRIM(tag.value) <> ''
GROUP BY tag.value
ORDER BY total DESC, tag.value
LIMIT ?`, append(args, tagLimit)); err != nil {
		return facets, fmt.Errorf("count sticker tags: %w", err)
	}
	return facets, nil
}

func (s *SQLiteStore) stickerFacetCounts(ctx context.Context, query string, args []any) ([]StickerFacetCount, error) {
	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := []StickerFacetCount{}
	for rows.Next() {
		var item StickerFacetCount
		if err := rows.Scan(&item.Value, &item.Count); err != nil {
			return nil, err
		}
		counts = append(counts, item)
	}
	return counts, rows.Err()
}

// StickerCleanupResult 是一次按条件清理的结果：Stickers 是涉及几张不同的图，Removed 是删掉几条会话记录。
type StickerCleanupResult struct {
	Stickers int  `json:"stickers"`
	Removed  int  `json:"removed"`
	DryRun   bool `json:"dry_run,omitempty"`
}

// CleanupStickerLibrary 把符合筛选条件的表情包移出池子。按来源筛时只移出这个来源的记录，
// 同一张图在别的群里的记录留着。block 为真时同时拉黑，以后再有人发也不收；dryRun 只数不删。
// 聊天记录里的图片不动。
func (s *SQLiteStore) CleanupStickerLibrary(ctx context.Context, query StickerLibraryQuery, block, dryRun bool) (StickerCleanupResult, error) {
	result := StickerCleanupResult{DryRun: dryRun}
	if s == nil || s.db == nil {
		return result, nil
	}
	base, args := stickerLibraryCTE(query, time.Now())
	selectRows := base + `SELECT s.row_id, s.content_sha256 FROM scoped AS s JOIN matched AS m ON m.content_sha256 = s.content_sha256`
	if dryRun {
		err := s.eventReader().QueryRowContext(ctx, base+`SELECT COUNT(*), COALESCE((SELECT COUNT(*) FROM scoped AS s JOIN matched AS m ON m.content_sha256 = s.content_sha256), 0) FROM matched`, args...).
			Scan(&result.Stickers, &result.Removed)
		if err != nil {
			return result, fmt.Errorf("count sticker cleanup: %w", err)
		}
		return result, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, selectRows, args...)
	if err != nil {
		return result, fmt.Errorf("select sticker cleanup: %w", err)
	}
	var rowIDs []any
	hashes := map[string]bool{}
	for rows.Next() {
		var rowID int64
		var hash string
		if err := rows.Scan(&rowID, &hash); err != nil {
			_ = rows.Close()
			return result, err
		}
		rowIDs = append(rowIDs, rowID)
		hashes[hash] = true
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for start := 0; start < len(rowIDs); start += 500 {
		batch := rowIDs[start:min(start+500, len(rowIDs))]
		if _, err := tx.ExecContext(ctx, `DELETE FROM sticker_assets WHERE rowid IN (`+sqlPlaceholders(len(batch))+`)`, batch...); err != nil {
			return result, fmt.Errorf("delete sticker cleanup: %w", err)
		}
	}
	if block {
		profileID := strings.TrimSpace(query.ProfileID)
		now := time.Now().Unix()
		for hash := range hashes {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO sticker_blocklist (profile_id, content_sha256, created_at) VALUES (?, ?, ?)
ON CONFLICT(profile_id, content_sha256) DO NOTHING`, profileID, hash, now); err != nil {
				return result, fmt.Errorf("block sticker cleanup: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Stickers, result.Removed = len(hashes), len(rowIDs)
	return result, nil
}

// DeleteStickerAsset 把一张表情包移出池子，并记进黑名单，之后再有人发同一张也不收。
// profileID 非空时只动这个机器人的；为空时对全部机器人生效。聊天记录里的图片不动。
func (s *SQLiteStore) DeleteStickerAsset(ctx context.Context, hash, profileID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if !validStickerAssetHash(hash) {
		return 0, nil
	}
	profileID = strings.TrimSpace(profileID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `DELETE FROM sticker_assets WHERE content_sha256 = ?`
	args := []any{hash}
	if profileID != "" {
		query += ` AND profile_id = ?`
		args = append(args, profileID)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete sticker asset: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sticker_blocklist (profile_id, content_sha256, created_at) VALUES (?, ?, ?)
ON CONFLICT(profile_id, content_sha256) DO NOTHING`, profileID, hash, time.Now().Unix()); err != nil {
		return 0, fmt.Errorf("block sticker asset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	removed, _ := result.RowsAffected()
	return int(removed), nil
}
