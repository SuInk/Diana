// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// message_speakers 是按会话汇总的发言人名录：同一账号用过的每个昵称各占一行。
//
// 它补的是「昵称 → 账号」这一跳。QQ 能现拉全群成员表，Telegram Bot API 拉不到，
// 通道内存里记住的近期发言者重启就没了，所以原来在 Telegram 里报个昵称，模型拿不到
// user_id，按人查聊天记录、按人召回记忆都卡在这一步。聊天记录本来就带着每条消息的
// 发送者昵称，汇总一份按会话索引的名录，昵称、旧昵称和 @用户名 都能解析回账号。
//
// 不从 message_events 现场 GROUP BY：大群一个会话几十万行，每次回复都扫一遍不划算。
const messageSpeakersSchema = `
CREATE TABLE IF NOT EXISTS message_speakers (
  session TEXT NOT NULL,
  user_id TEXT NOT NULL,
  sender_name TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (session, user_id, sender_name)
);
CREATE INDEX IF NOT EXISTS idx_message_speakers_session_seen ON message_speakers(session, last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_message_speakers_user ON message_speakers(user_id, session);
`

// messageSpeakersBackfillKey 记录存量回填推进到的 message_events rowid；做完存 -1。
const messageSpeakersBackfillKey = "message_speakers_backfill_cursor"

const messageSpeakersBackfillBatch = 5000

const messageSpeakerUpsert = `
INSERT INTO message_speakers (session, user_id, sender_name, username, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(session, user_id, sender_name) DO UPDATE SET
  username = CASE WHEN excluded.last_seen >= message_speakers.last_seen AND excluded.username != '' THEN excluded.username ELSE message_speakers.username END,
  first_seen = MIN(message_speakers.first_seen, excluded.first_seen),
  last_seen = MAX(message_speakers.last_seen, excluded.last_seen)`

func (s *SQLiteStore) migrateMessageSpeakers() error {
	if _, err := s.db.Exec(messageSpeakersSchema); err != nil {
		return fmt.Errorf("create message speakers schema: %w", err)
	}
	return nil
}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// recordMessageSpeakerTx 把一条入站消息的发送者记进名录。机器人自己的出站消息、
// 通知和没有账号的事件不记：名录是给「找人」用的。
func recordMessageSpeakerTx(ctx context.Context, exec sqlExecer, session string, event assistant.MessageEvent, eventTime int64) error {
	if event.Outbound || event.Kind == assistant.EventKindNotice {
		return nil
	}
	return upsertMessageSpeaker(ctx, exec, session, event.UserID, event.SenderName, event.SenderUsername, eventTime)
}

func upsertMessageSpeaker(ctx context.Context, exec sqlExecer, session, userID, name, username string, at int64) error {
	session, userID = strings.TrimSpace(session), strings.TrimSpace(userID)
	name, username = strings.TrimSpace(name), strings.TrimPrefix(strings.TrimSpace(username), "@")
	if session == "" || userID == "" {
		return nil
	}
	if name == "" {
		name = firstNonEmptyString(username, userID)
	}
	_, err := exec.ExecContext(ctx, messageSpeakerUpsert, session, userID, name, username, at, at)
	return err
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// messageSpeakersBackfillPending 报告存量历史是否还没汇总完，返回下一批的起点。
func (s *SQLiteStore) messageSpeakersBackfillPending(ctx context.Context) (int64, bool) {
	var cursor int64
	ok, err := s.loadJSON(ctx, messageSpeakersBackfillKey, &cursor)
	if err != nil {
		return 0, false
	}
	if !ok {
		return 0, true
	}
	return cursor, cursor >= 0
}

// backfillMessageSpeakersBatch 按 rowid 往后汇总一批存量消息，返回下一批游标和是否做完。
// 升级到带名录的版本之前的历史只能这样补；新消息在写入时同步记。
func (s *SQLiteStore) backfillMessageSpeakersBatch(ctx context.Context, after int64) (int64, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT rowid, session, kind, COALESCE(user_id, ''), COALESCE(sender_name, ''), event_time,
  COALESCE(CASE WHEN json_valid(payload) THEN json_extract(payload, '$.sender_username') END, ''),
  COALESCE(CASE WHEN json_valid(payload) THEN json_extract(payload, '$.outbound') END, 0)
FROM message_events WHERE rowid > ? ORDER BY rowid LIMIT ?`, after, messageSpeakersBackfillBatch)
	if err != nil {
		return after, false, err
	}
	type pending struct {
		session, userID, name, username string
		at                              int64
	}
	var batch []pending
	next := after
	for rows.Next() {
		var rowID, at, outbound int64
		var session, kind, userID, name, username string
		if err := rows.Scan(&rowID, &session, &kind, &userID, &name, &at, &username, &outbound); err != nil {
			_ = rows.Close()
			return after, false, err
		}
		next = rowID
		if outbound != 0 || kind == string(assistant.EventKindNotice) {
			continue
		}
		batch = append(batch, pending{session, userID, name, username, at})
	}
	closeErr := rows.Close()
	if err := rows.Err(); err != nil || closeErr != nil {
		return after, false, errors.Join(err, closeErr)
	}
	if next == after {
		return after, true, s.saveJSON(ctx, messageSpeakersBackfillKey, int64(-1))
	}
	tx, err := s.beginWriteTx(ctx, "backfillMessageSpeakers")
	if err != nil {
		return after, false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range batch {
		if err := upsertMessageSpeaker(ctx, tx, item.session, item.userID, item.name, item.username, item.at); err != nil {
			return after, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return after, false, err
	}
	return next, false, s.saveJSON(ctx, messageSpeakersBackfillKey, next)
}

// BackfillMessageSpeakers 一次做完存量回填。启动时由后台巡检分批推进，这里给没有
// 后台巡检的库（内存库、测试）用。
func (s *SQLiteStore) BackfillMessageSpeakers(ctx context.Context) error {
	cursor, pending := s.messageSpeakersBackfillPending(ctx)
	for pending {
		next, done, err := s.backfillMessageSpeakersBatch(ctx, cursor)
		if err != nil {
			return err
		}
		cursor, pending = next, !done
	}
	return nil
}

// ListMessageSpeakers 在名录里按昵称（含旧昵称）、@用户名或账号找人。Name 为空时
// 按最近发言列出会话里的人。同一账号的多个昵称合并成一条，最近用的排前面。
func (s *SQLiteStore) ListMessageSpeakers(ctx context.Context, query assistant.MessageSpeakerQuery) ([]assistant.MessageSpeaker, error) {
	defer s.observeStorage(ctx, "ListMessageSpeakers", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	session, prefix := strings.TrimSpace(query.Session), strings.TrimSpace(query.SessionPrefix)
	if (!query.CrossSession && session == "") || (query.CrossSession && prefix == "") {
		return nil, nil
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	where := `session = ?`
	args := []any{session}
	if query.CrossSession {
		where = `session LIKE ? ESCAPE '\'`
		args = []any{escapeMessageHistoryLike(prefix) + "%"}
	}
	name := strings.ToLower(strings.TrimSpace(query.Name))
	handle := strings.TrimPrefix(name, "@")
	if name != "" {
		where += ` AND (LOWER(sender_name) LIKE ? ESCAPE '\' OR LOWER(username) = ? OR user_id = ?)`
		args = append(args, "%"+escapeMessageHistoryLike(name)+"%", handle, strings.TrimSpace(query.Name))
	}
	// 先找出命中的账号，再把这些账号在同一范围内用过的全部昵称取回来：只按命中的
	// 那一行展示，模型就看不出「小明」和「明明」其实是同一个人。
	idRows, err := s.eventReader().QueryContext(ctx, `
SELECT user_id, MAX(last_seen) AS seen FROM message_speakers WHERE `+where+`
GROUP BY user_id ORDER BY seen DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var ids []any
	for idRows.Next() {
		var id string
		var seen int64
		if err := idRows.Scan(&id, &seen); err != nil {
			_ = idRows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	closeErr := idRows.Close()
	if err := idRows.Err(); err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	scope := `session = ?`
	scopeArgs := []any{session}
	if query.CrossSession {
		scope = `session LIKE ? ESCAPE '\'`
		scopeArgs = []any{escapeMessageHistoryLike(prefix) + "%"}
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT user_id, sender_name, username, first_seen, last_seen FROM message_speakers
WHERE `+scope+` AND user_id IN (`+placeholders(len(ids))+`)
ORDER BY last_seen DESC`, append(scopeArgs, ids...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byID := map[string]*assistant.MessageSpeaker{}
	for rows.Next() {
		var userID, senderName, username string
		var first, last int64
		if err := rows.Scan(&userID, &senderName, &username, &first, &last); err != nil {
			return nil, err
		}
		speaker := byID[userID]
		if speaker == nil {
			speaker = &assistant.MessageSpeaker{UserID: userID, FirstSeen: first, LastSeen: last}
			byID[userID] = speaker
		}
		if !containsString(speaker.Names, senderName) {
			speaker.Names = append(speaker.Names, senderName)
		}
		if speaker.Username == "" {
			speaker.Username = username
		}
		speaker.FirstSeen = min(speaker.FirstSeen, first)
		speaker.LastSeen = max(speaker.LastSeen, last)
		if name != "" && (strings.EqualFold(senderName, strings.TrimSpace(query.Name)) || (handle != "" && strings.EqualFold(username, handle)) || userID == strings.TrimSpace(query.Name)) {
			speaker.ExactMatch = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]assistant.MessageSpeaker, 0, len(byID))
	for _, speaker := range byID {
		out = append(out, *speaker)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ExactMatch != out[j].ExactMatch {
			return out[i].ExactMatch
		}
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
