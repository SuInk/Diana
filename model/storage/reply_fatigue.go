// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"

	"github.com/SuInk/diana/model/assistant"
)

// replyFatigueKey 存回复疲劳的快慢两档和攒着的目的分。记录自带时间，消退由
// assistant 按时间算，这里只负责原样存取。
const replyFatigueKey = "reply_fatigue"

func (s *SQLiteStore) LoadReplyFatigue(ctx context.Context) ([]assistant.ReplyFatigueRecord, error) {
	var records []assistant.ReplyFatigueRecord
	ok, err := s.loadJSON(ctx, replyFatigueKey, &records)
	if err != nil || !ok {
		return nil, err
	}
	return records, nil
}

func (s *SQLiteStore) SaveReplyFatigue(ctx context.Context, records []assistant.ReplyFatigueRecord) error {
	return s.saveJSON(ctx, replyFatigueKey, records)
}
