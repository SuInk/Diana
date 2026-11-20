// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// message_reactions 记的是「现在」的状态：一行表示某人此刻对某条消息挂着某个表情。
// 取消了就删掉，所以统计出来的是还挂着的表情，不是历史上点过又撤掉的次数。
const messageReactionsSchema = `
CREATE TABLE IF NOT EXISTS message_reactions (
  session TEXT NOT NULL,
  message_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  emoji TEXT NOT NULL,
  platform TEXT NOT NULL DEFAULT '',
  user_name TEXT NOT NULL DEFAULT '',
  reacted_at INTEGER NOT NULL,
  PRIMARY KEY (session, message_id, user_id, emoji)
);
CREATE INDEX IF NOT EXISTS idx_message_reactions_session_time ON message_reactions(session, reacted_at DESC);
`

func (s *SQLiteStore) migrateMessageReactions() error {
	if _, err := s.db.Exec(messageReactionsSchema); err != nil {
		return fmt.Errorf("create message reactions schema: %w", err)
	}
	return nil
}

// RecordMessageReaction 把一次表情回应的变化落库。
func (s *SQLiteStore) RecordMessageReaction(ctx context.Context, reaction assistant.MessageReaction) error {
	defer s.observeStorage(ctx, "RecordMessageReaction", "write")()
	if s == nil || s.db == nil {
		return nil
	}
	session := strings.TrimSpace(reaction.Session)
	messageID := strings.TrimSpace(reaction.MessageID)
	userID := strings.TrimSpace(reaction.UserID)
	if session == "" || messageID == "" || userID == "" {
		return fmt.Errorf("表情回应缺少会话、消息或用户")
	}
	at := reaction.At
	if at <= 0 {
		at = time.Now().Unix()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if reaction.Replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_reactions WHERE session = ? AND message_id = ? AND user_id = ?`, session, messageID, userID); err != nil {
			return err
		}
	}
	for _, emoji := range reaction.Emojis {
		emoji = strings.TrimSpace(emoji)
		if emoji == "" {
			continue
		}
		if !reaction.Replace && !reaction.Added {
			if _, err := tx.ExecContext(ctx, `DELETE FROM message_reactions WHERE session = ? AND message_id = ? AND user_id = ? AND emoji = ?`, session, messageID, userID, emoji); err != nil {
				return err
			}
			continue
		}
		// 同一个表情重复贴上只刷新名字，不改时间：时间是第一次挂上的时候。
		if _, err := tx.ExecContext(ctx, `
INSERT INTO message_reactions (session, message_id, user_id, emoji, platform, user_name, reacted_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session, message_id, user_id, emoji) DO UPDATE SET
  user_name = CASE WHEN excluded.user_name != '' THEN excluded.user_name ELSE message_reactions.user_name END`,
			session, messageID, userID, emoji, strings.TrimSpace(reaction.Platform), strings.TrimSpace(reaction.UserName), at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListMessageReactions 列出某条消息上现在挂着的表情，按挂上的先后排。
func (s *SQLiteStore) ListMessageReactions(ctx context.Context, session, messageID string) ([]assistant.MessageReactionRow, error) {
	defer s.observeStorage(ctx, "ListMessageReactions", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT message_id, user_id, user_name, emoji, reacted_at FROM message_reactions
WHERE session = ? AND message_id = ?
ORDER BY reacted_at, user_id, emoji`, strings.TrimSpace(session), strings.TrimSpace(messageID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []assistant.MessageReactionRow
	for rows.Next() {
		var row assistant.MessageReactionRow
		if err := rows.Scan(&row.MessageID, &row.UserID, &row.UserName, &row.Emoji, &row.At); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// RankReactionGivers 统计时间段里谁挂的表情最多。一条消息上挂两个表情算两次。
// 按会话分开计数，跨群合并由调用方做（要先按权限筛掉读不了的群）。
func (s *SQLiteStore) RankReactionGivers(ctx context.Context, query assistant.GroupStatsQuery) ([]assistant.GroupStatsRank, error) {
	defer s.observeStorage(ctx, "RankReactionGivers", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	return s.groupStatsRank(ctx, `
SELECT session AS rank_session, user_id AS rank_user, MAX(user_name) AS rank_name, COUNT(*) AS rank_count FROM message_reactions
WHERE %s AND reacted_at >= ? AND reacted_at <= ?
GROUP BY session, user_id`, query)
}

// RankGroupSpeakers 统计时间段里谁发言最多，数的是本地记下的群消息条数。
// 名字取这个人在该群最近一条消息上的群名片或昵称。按会话分开计数，同 RankReactionGivers。
func (s *SQLiteStore) RankGroupSpeakers(ctx context.Context, query assistant.GroupStatsQuery) ([]assistant.GroupStatsRank, error) {
	defer s.observeStorage(ctx, "RankGroupSpeakers", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	return s.groupStatsRank(ctx, `
SELECT counted.session AS rank_session, counted.user_id AS rank_user,
       (SELECT latest.sender_name FROM message_events AS latest
        WHERE latest.session = counted.session AND latest.user_id = counted.user_id AND latest.kind = 'group'
        ORDER BY latest.event_time DESC LIMIT 1) AS rank_name,
       COUNT(*) AS rank_count
FROM message_events AS counted
WHERE %s AND counted.kind = 'group' AND counted.event_time >= ? AND counted.event_time <= ?
  AND COALESCE(counted.user_id, '') != ''
GROUP BY counted.session, counted.user_id`, query)
}

// groupStatsRank 跑一条「按会话和人分组计数」的查询，按条数从多到少返回全部行。
// grouped 里的 %s 填会话条件：给了 SessionPrefix 就是这个前缀下的所有群，否则是单个 Session。
func (s *SQLiteStore) groupStatsRank(ctx context.Context, grouped string, query assistant.GroupStatsQuery) ([]assistant.GroupStatsRank, error) {
	session, prefix := strings.TrimSpace(query.Session), strings.TrimSpace(query.SessionPrefix)
	condition, target := "session = ?", session
	if prefix != "" {
		condition, target = `session LIKE ? ESCAPE '\'`, escapeMessageHistoryLike(prefix)+"%"
	} else if session == "" {
		return nil, nil
	}
	if strings.Contains(grouped, "counted.") {
		condition = "counted." + condition
	}
	until := query.Until
	if until <= 0 {
		until = time.Now().Unix()
	}
	// 每条分组查询都把列命名成 rank_session / rank_user / rank_name / rank_count，这里统一排序。
	rows, err := s.eventReader().QueryContext(ctx, `SELECT rank_session, rank_user, COALESCE(rank_name, ''), rank_count FROM (`+fmt.Sprintf(grouped, condition)+`)
ORDER BY rank_count DESC, rank_user, rank_session`, target, query.Since, until)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var all []assistant.GroupStatsRank
	for rows.Next() {
		var rank assistant.GroupStatsRank
		if err := rows.Scan(&rank.Session, &rank.UserID, &rank.UserName, &rank.Count); err != nil {
			return nil, err
		}
		all = append(all, rank)
	}
	return all, rows.Err()
}
