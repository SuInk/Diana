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
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  kind=excluded.kind,
  group_id=excluded.group_id,
  user_id=excluded.user_id,
  message_id=excluded.message_id,
  sender_name=excluded.sender_name,
  event_time=excluded.event_time,
  text=excluded.text,
  payload=excluded.payload,
  created_at=excluded.created_at
`, id, session, string(event.Kind), event.GroupID, event.UserID, event.MessageID, event.SenderName, eventTime, text, string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
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

	searchable := `LOWER(COALESCE(sender_name, '') || CHAR(10) || COALESCE(user_id, '') || CHAR(10) || COALESCE(message_id, '') || CHAR(10) || COALESCE(group_id, '') || CHAR(10) || COALESCE(text, '') || CHAR(10) || payload)`
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
