// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// 少实现一个方法不会报错，只会让 runtime 里那个类型断言悄悄失败——连带把发送前
// 的「已并入别的回复轮」检查一起停掉。编译期钉住，别让它变成运行时才发现的事。
var _ assistant.InboundSupersessionStore = (*SQLiteStore)(nil)
var _ assistant.InboundBacklogStore = (*SQLiteStore)(nil)

// InboundEventSuperseded 报告这条入站消息是否已被标记为并入另一轮回复。
//
// 标记现在只由追发合并写入（RecordInboundEventReplyMerge）。旧版相邻媒体合并留下的
// superseded_by 行照样读得出来：那些任务早已落终态，读到也只是在发送前多拦一次。
func (s *SQLiteStore) InboundEventSuperseded(ctx context.Context, event assistant.MessageEvent) (string, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(event.MessageID) == "" {
		return "", false, nil
	}
	var supersededBy sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT superseded_by
FROM inbound_events
WHERE message_id = ? AND kind = ?
  AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
ORDER BY created_at DESC, id DESC LIMIT 1
`, strings.TrimSpace(event.MessageID), string(event.Kind), strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID)).Scan(&supersededBy)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("check inbound event supersession: %w", err)
	}
	value := strings.TrimSpace(supersededBy.String)
	return value, value != "", nil
}
