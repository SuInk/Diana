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

	"github.com/google/uuid"
)

const (
	defaultMessageHistoryLimit = 20
	// Prompt history candidates are loaded from a token-derived limit. Keep this
	// ceiling high enough for large windows containing many short chat messages;
	// the assistant still applies its token budget before constructing a prompt.
	maxMessageHistoryLimit = 4096
)

// AppendMessageEvent persists an inbound message event for later context recovery.
func (s *SQLiteStore) AppendMessageEvent(ctx context.Context, session string, event assistant.MessageEvent) error {
	if s == nil || s.db == nil {
		return nil
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	id := persistedMessageID(session, event)
	eventTime := event.Time
	if eventTime <= 0 {
		eventTime = time.Now().Unix()
	}
	text := strings.TrimSpace(assistant.PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO message_events (id, session, kind, profile_id, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  kind=excluded.kind,
  profile_id=excluded.profile_id,
  group_id=excluded.group_id,
  user_id=excluded.user_id,
  message_id=excluded.message_id,
  sender_name=excluded.sender_name,
  event_time=excluded.event_time,
  text=excluded.text,
  payload=excluded.payload,
  created_at=excluded.created_at
`, id, session, string(event.Kind), strings.TrimSpace(event.ProfileID), event.GroupID, event.UserID, event.MessageID, event.SenderName, eventTime, text, string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	// 检索 token 要在 Go 侧切分，触发器做不到，所以写入后同步一次索引。
	// 同一条消息重复入库时 search_extra 保持原值（图片描述是后到的，不能被覆盖掉）。
	s.indexMessageHistoryRow(id, event.SenderName, event.UserID, text, s.messageSearchExtra(ctx, id))
	if err := s.indexStickerAssets(ctx, session, event); err != nil {
		return fmt.Errorf("index sticker assets: %w", err)
	}
	return nil
}

// messageSearchExtra 读取某条消息已有的检索附加文本。
func (s *SQLiteStore) messageSearchExtra(ctx context.Context, id string) string {
	var extra sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT search_extra FROM message_events WHERE id = ?`, id).Scan(&extra); err != nil {
		return ""
	}
	return strings.TrimSpace(extra.String)
}

// SaveMessageSearchExtra 记录一条消息「正文之外还能被搜到的文本」，目前是图片
// 描述。描述由后台视觉调用异步生成，消息早就落库了，所以只能事后补写；写完立刻
// 重建这一行的检索索引，否则新描述要等下次消息更新才进得去。
func (s *SQLiteStore) SaveMessageSearchExtra(ctx context.Context, session, messageID, extra string) error {
	if s == nil || s.db == nil {
		return nil
	}
	session, messageID = strings.TrimSpace(session), strings.TrimSpace(messageID)
	extra = strings.TrimSpace(extra)
	if session == "" || messageID == "" || extra == "" {
		return nil
	}
	var (
		id     string
		sender sql.NullString
		userID sql.NullString
		text   sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, sender_name, user_id, text
FROM message_events
WHERE session = ? AND message_id = ?
ORDER BY event_time DESC, created_at DESC
LIMIT 1
`, session, messageID).Scan(&id, &sender, &userID, &text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load message for search extra: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE message_events SET search_extra = ? WHERE id = ?`, extra, id); err != nil {
		return fmt.Errorf("save message search extra: %w", err)
	}
	s.indexMessageHistoryRow(id, sender.String, userID.String, text.String, extra)
	return nil
}

// ListRecentMessageEvents returns recent message events in chronological order.
func (s *SQLiteStore) ListRecentMessageEvents(ctx context.Context, session string, limit int) ([]assistant.MessageEvent, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return nil, nil
	}
	limit = normalizeMessageHistoryLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT payload
FROM message_events
WHERE session = ? AND kind != ?
ORDER BY event_time DESC, created_at DESC, id DESC
LIMIT ?
`, session, string(assistant.EventKindNotice), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	reversed := make([]assistant.MessageEvent, 0, limit)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("decode message event: %w", err)
		}
		reversed = append(reversed, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed, nil
}

// ListRecentStickerEvents provides the sticker plugin's bounded, read-only history view.
// Shared mode stays inside one context namespace or bot profile. It needs no sticker-specific
// table because image segments are already durable.
func (s *SQLiteStore) ListRecentStickerEvents(ctx context.Context, query assistant.StickerHistoryQuery) ([]assistant.MessageEvent, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	query.Session = strings.TrimSpace(query.Session)
	query.ContextNamespace = strings.TrimSpace(query.ContextNamespace)
	query.ProfileID = strings.TrimSpace(query.ProfileID)
	limit := normalizeMessageHistoryLimit(query.Limit)
	current, err := s.queryRecentStickerEvents(ctx, "session = ?", []any{query.Session}, limit)
	if err != nil || (!query.ShareGroups && !query.SharePrivate) {
		return current, err
	}

	boundary := ""
	args := make([]any, 0, 4)
	switch {
	case query.ContextNamespace != "":
		boundary = "session LIKE ? ESCAPE '\\'"
		args = append(args, escapeMessageHistoryLike(query.ContextNamespace+":")+"%")
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
	shared, err := s.queryRecentStickerEvents(ctx, boundary+" AND session != ? AND ("+strings.Join(scopes, " OR ")+")", args, limit)
	if err != nil {
		return nil, err
	}
	return append(current, shared...), nil
}

func (s *SQLiteStore) queryRecentStickerEvents(ctx context.Context, where string, args []any, limit int) ([]assistant.MessageEvent, error) {
	args = append(args, string(assistant.EventKindNotice), limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT payload
FROM message_events
WHERE `+where+` AND kind != ?
ORDER BY event_time DESC, created_at DESC, id DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]assistant.MessageEvent, 0, limit)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("decode sticker message event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}

// ListMessageEventsBetween returns the complete persisted timeline inside a
// semantic time window. Callers are responsible for ranking a bounded set of
// candidates before sending anything to an LLM.
func (s *SQLiteStore) ListMessageEventsBetween(ctx context.Context, session string, fromTime, throughTime int64) ([]assistant.MessageEvent, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return nil, nil
	}
	if fromTime < 0 {
		fromTime = 0
	}
	if throughTime <= 0 {
		throughTime = time.Now().Unix()
	}
	if fromTime > throughTime {
		fromTime, throughTime = throughTime, fromTime
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT payload
FROM message_events
WHERE session = ?
  AND kind != ?
  AND event_time BETWEEN ? AND ?
ORDER BY event_time ASC, created_at ASC, id ASC
`, session, string(assistant.EventKindNotice), fromTime, throughTime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]assistant.MessageEvent, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("decode message event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// SearchMessageEvents performs a bounded database-side search over durable
// history. Cross-session searches are restricted to an explicit session prefix
// supplied by the runtime, so records from another bot namespace cannot leak in.
func (s *SQLiteStore) SearchMessageEvents(ctx context.Context, query assistant.MessageHistorySearchQuery) ([]assistant.MessageEvent, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, nil
	}
	query.Session = strings.TrimSpace(query.Session)
	query.SessionPrefix = strings.TrimSpace(query.SessionPrefix)
	query.Text = strings.TrimSpace(query.Text)
	if query.Text == "" || (!query.CrossSession && query.Session == "") || (query.CrossSession && query.SessionPrefix == "") {
		return nil, 0, nil
	}
	if query.FromTime < 0 {
		query.FromTime = 0
	}
	if query.ThroughTime <= 0 {
		query.ThroughTime = time.Now().Unix()
	}
	if query.FromTime > query.ThroughTime {
		query.FromTime, query.ThroughTime = query.ThroughTime, query.FromTime
	}
	limit := normalizeMessageHistoryLimit(query.Limit)

	searchable := `LOWER(COALESCE(sender_name, '') || CHAR(10) || COALESCE(user_id, '') || CHAR(10) || COALESCE(message_id, '') || CHAR(10) || COALESCE(group_id, '') || CHAR(10) || COALESCE(text, '') || CHAR(10) || COALESCE(search_extra, '') || CHAR(10) || payload)`
	where := `kind != ? AND event_time BETWEEN ? AND ?`
	args := []any{string(assistant.EventKindNotice), query.FromTime, query.ThroughTime}
	if query.CrossSession {
		where += ` AND session LIKE ? ESCAPE '\'`
		args = append(args, escapeMessageHistoryLike(query.SessionPrefix)+"%")
	} else {
		where += ` AND session = ?`
		args = append(args, query.Session)
	}
	terms := historySearchTerms(query)
	if s.historyFTS {
		if events, total, ok, err := s.searchMessageEventsFTS(ctx, where, args, terms, limit); ok {
			return events, total, err
		}
	}
	matchParts := make([]string, 0, len(terms))
	for _, term := range terms {
		matchParts = append(matchParts, searchable+` LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeMessageHistoryLike(term)+"%")
	}
	where += ` AND (` + strings.Join(matchParts, ` OR `) + `)`

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_events WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	scoreParts := make([]string, 0, len(terms))
	scoreArgs := make([]any, 0, len(terms))
	for index, term := range terms {
		weight := 1
		if index == 0 {
			weight = 8
		}
		scoreParts = append(scoreParts, fmt.Sprintf(`CASE WHEN %s LIKE ? ESCAPE '\' THEN %d ELSE 0 END`, searchable, weight))
		scoreArgs = append(scoreArgs, "%"+escapeMessageHistoryLike(term)+"%")
	}
	rowArgs := append(append([]any(nil), args...), scoreArgs...)
	rowArgs = append(rowArgs, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT payload
FROM message_events
WHERE `+where+`
ORDER BY (`+strings.Join(scoreParts, ` + `)+`) DESC, event_time DESC, created_at DESC, id DESC
LIMIT ?`, rowArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]assistant.MessageEvent, 0, min(limit, total))
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, 0, fmt.Errorf("decode message event: %w", err)
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

func historySearchTerms(query assistant.MessageHistorySearchQuery) []string {
	seen := make(map[string]struct{})
	terms := make([]string, 0, min(49, len(query.Terms)+1))
	add := func(term string) {
		term = strings.TrimSpace(strings.ToLower(term))
		if len([]rune(term)) < 2 {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	add(strings.Join(strings.Fields(query.Text), ""))
	for _, term := range query.Terms {
		add(term)
	}
	for _, term := range strings.Fields(query.Text) {
		add(term)
	}
	if len(terms) == 0 {
		terms = append(terms, strings.ToLower(strings.TrimSpace(query.Text)))
	}
	if len(terms) > 49 {
		terms = terms[:49]
	}
	return terms
}

func escapeMessageHistoryLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// FindMessageEvent returns the persisted non-notice message with the given OneBot message ID.
func (s *SQLiteStore) FindMessageEvent(ctx context.Context, session string, messageID string) (assistant.MessageEvent, bool, error) {
	if s == nil || s.db == nil {
		return assistant.MessageEvent{}, false, nil
	}
	session = strings.TrimSpace(session)
	messageID = strings.TrimSpace(messageID)
	if session == "" || messageID == "" {
		return assistant.MessageEvent{}, false, nil
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `
SELECT payload
FROM message_events
WHERE session = ? AND message_id = ? AND kind != ?
ORDER BY event_time DESC, created_at DESC, id DESC
LIMIT 1
`, session, messageID, string(assistant.EventKindNotice)).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return assistant.MessageEvent{}, false, nil
		}
		return assistant.MessageEvent{}, false, err
	}
	var event assistant.MessageEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return assistant.MessageEvent{}, false, fmt.Errorf("decode message event: %w", err)
	}
	return event, true, nil
}

// ListGroupRecallEvents returns every persisted group recall, newest first.
func (s *SQLiteStore) ListGroupRecallEvents(ctx context.Context, groupID string) ([]assistant.MessageEvent, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT recall.payload,
       (SELECT original.event_time
        FROM message_events AS original
        WHERE original.session = recall.session
          AND original.message_id = recall.message_id
          AND original.kind != ?
        ORDER BY original.event_time DESC, original.created_at DESC, original.id DESC
        LIMIT 1) AS original_time
FROM message_events AS recall
WHERE recall.kind = ? AND recall.group_id = ? AND json_extract(recall.payload, '$.sub_type') = 'group_recall'
ORDER BY recall.event_time DESC, recall.created_at DESC, recall.id DESC
`, string(assistant.EventKindNotice), string(assistant.EventKindNotice), groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := make([]assistant.MessageEvent, 0)
	for rows.Next() {
		var raw string
		var originalTime sql.NullInt64
		if err := rows.Scan(&raw, &originalTime); err != nil {
			return nil, err
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("decode recall event: %w", err)
		}
		if event.OriginalTime == 0 && originalTime.Valid {
			event.OriginalTime = originalTime.Int64
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func persistedMessageID(session string, event assistant.MessageEvent) string {
	if strings.TrimSpace(event.MessageID) != "" {
		if event.Kind == assistant.EventKindNotice {
			return session + ":notice:" + strings.TrimSpace(event.SubType) + ":" + strings.TrimSpace(event.MessageID)
		}
		return session + ":" + strings.TrimSpace(event.MessageID)
	}
	return session + ":" + uuid.NewString()
}

func normalizeMessageHistoryLimit(limit int) int {
	if limit <= 0 {
		return defaultMessageHistoryLimit
	}
	if limit > maxMessageHistoryLimit {
		return maxMessageHistoryLimit
	}
	return limit
}

// searchMessageEventsFTS 走 FTS5 倒排索引，按 BM25 相关度排序。
//
// trigram 分词器查不了短于 3 个字符的词，而中文 2 字词（凤爪、好吃）很常见，
// 全丢掉召回会明显变差。所以短词仍旧用 LIKE，与索引结果取并集：命中索引的
// 按 BM25 排前面，只靠短词命中的按时间排在后面。
//
// 第三个返回值为 false 表示这次用不了索引，调用方回退到原来的 LIKE 检索。
func (s *SQLiteStore) searchMessageEventsFTS(ctx context.Context, where string, args []any, terms []string, limit int) ([]assistant.MessageEvent, int, bool, error) {
	match := messageHistoryFTSQuery(terms)
	if match == "" {
		return nil, 0, false, nil
	}
	scopedWhere := prefixMessageHistoryColumns(where)

	// MATCH 放进 CTE 只求值一次。写成逐行的相关子查询会让每个候选都重跑一遍
	// 全文检索，实测比原来的 LIKE 还慢一个数量级。
	hits := `WITH hits AS (SELECT rowid AS rid, bm25(` + messageHistoryFTSTable + `) AS score
FROM ` + messageHistoryFTSTable + ` WHERE ` + messageHistoryFTSTable + ` MATCH ?)`

	hitArgs := []any{match}
	hit := `h.rid IS NOT NULL`
	from := `FROM message_events AS e JOIN hits AS h ON h.rid = e.rowid`

	countArgs := append(append([]any(nil), hitArgs...), args...)
	var total int
	if err := s.db.QueryRowContext(ctx,
		hits+` SELECT COUNT(*) `+from+` WHERE `+hit+` AND `+scopedWhere, countArgs...).Scan(&total); err != nil {
		// 索引出问题时不要让检索整个失败，交回 LIKE 那一路。
		return nil, 0, false, nil
	}

	rowArgs := append(append([]any(nil), hitArgs...), args...)
	rowArgs = append(rowArgs, limit)
	rows, err := s.db.QueryContext(ctx, hits+`
SELECT e.payload `+from+`
WHERE `+hit+` AND `+scopedWhere+`
ORDER BY h.score ASC, e.event_time DESC, e.created_at DESC, e.id DESC
LIMIT ?`, rowArgs...)
	if err != nil {
		return nil, 0, false, nil
	}
	defer func() { _ = rows.Close() }()
	events := make([]assistant.MessageEvent, 0, min(limit, total))
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, 0, true, err
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, true, err
	}
	return events, total, true, nil
}

// prefixMessageHistoryColumns 给 where 子句里的列名加上 e. 前缀。
// 拼 where 的地方就在本文件里，列名固定是这几个，不接受外部输入。
func prefixMessageHistoryColumns(where string) string {
	for _, column := range []string{"kind", "event_time", "session", "sender_name", "user_id", "message_id", "group_id", "text", "payload"} {
		where = strings.ReplaceAll(where, "COALESCE("+column+",", "COALESCE(e."+column+",")
		where = strings.ReplaceAll(where, column+" ", "e."+column+" ")
	}
	return strings.ReplaceAll(where, "|| payload)", "|| e.payload)")
}
