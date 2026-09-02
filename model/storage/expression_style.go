// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 表达学习的计数表。一行是「某个群里的某句短表达被说过多少次」，机器人拿排行
// 靠前的当说话风格参考。表达是会过气的，所以查询只看最近一段时间，过期行由
// PruneGroupExpressions 清掉。

// BumpGroupExpression 给一条表达计一次数。
//
// user_count 是「换过几个人说」的近似：只有说话的人和上一次不同才加一。它挡的
// 是一个人刷同一句话把它刷成「群常用语」；两个人轮流刷仍然骗得过它，但那已经
// 是两个人的共谋，学进去也不冤。
func (s *SQLiteStore) BumpGroupExpression(ctx context.Context, scopeKey, phrase, userID string, seenAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	scopeKey, phrase = strings.TrimSpace(scopeKey), strings.TrimSpace(phrase)
	if scopeKey == "" || phrase == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO group_expressions (scope_key, phrase, count, user_count, last_user_id, first_seen_at, last_seen_at)
VALUES (?, ?, 1, 1, ?, ?, ?)
ON CONFLICT(scope_key, phrase) DO UPDATE SET
  count = count + 1,
  user_count = user_count + (CASE WHEN last_user_id = excluded.last_user_id THEN 0 ELSE 1 END),
  last_user_id = excluded.last_user_id,
  last_seen_at = excluded.last_seen_at
`, scopeKey, phrase, strings.TrimSpace(userID), seenAt.Unix(), seenAt.Unix())
	return err
}

// TopGroupExpressions 取一个群最近的高频表达，按次数倒序。
func (s *SQLiteStore) TopGroupExpressions(ctx context.Context, scopeKey string, since time.Time, minCount, minUsers, limit int) ([]assistant.GroupExpression, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT phrase, count
FROM group_expressions
WHERE scope_key = ? AND last_seen_at >= ? AND count >= ? AND user_count >= ?
ORDER BY count DESC, last_seen_at DESC
LIMIT ?
`, strings.TrimSpace(scopeKey), since.Unix(), minCount, minUsers, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	expressions := make([]assistant.GroupExpression, 0, limit)
	for rows.Next() {
		var expression assistant.GroupExpression
		if err := rows.Scan(&expression.Phrase, &expression.Count); err != nil {
			return nil, err
		}
		expressions = append(expressions, expression)
	}
	return expressions, rows.Err()
}

// PruneGroupExpressions 清掉太久没人说的表达。
func (s *SQLiteStore) PruneGroupExpressions(ctx context.Context, before time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM group_expressions WHERE last_seen_at < ?`, before.Unix())
	return err
}
