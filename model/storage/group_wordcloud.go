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

// groupWordCloudScanLimit 和关系图同一个道理：词频的头部在几万条上就稳定了。
const groupWordCloudScanLimit = 30000

// GroupWordCloudFor 统计一个群一段时间内的高频词。机器人自己的发言（outbound
// 或者就是 botID 发的）不算：词云要看的是群友在聊什么。
func (s *SQLiteStore) GroupWordCloudFor(ctx context.Context, groupID string, since time.Time, botID string) (assistant.GroupWordCloud, error) {
	cloud := assistant.GroupWordCloud{GroupID: strings.TrimSpace(groupID), Words: []assistant.GroupWordCloudWord{}}
	counter := assistant.NewWordCloudCounter()
	cloud.Segmenter = counter.Segmenter()
	if s == nil || s.db == nil || cloud.GroupID == "" {
		return cloud, nil
	}
	sinceUnix := int64(0)
	if !since.IsZero() {
		at := since
		cloud.Since = &at
		sinceUnix = since.Unix()
	}
	botID = strings.TrimSpace(botID)

	// 走 idx_message_events_kind_group_time；撤回通知会以 :notice: 行再存一份
	// 原文，不排除的话撤回过的消息算两遍。
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT COALESCE(user_id, ''), COALESCE(text, '')
FROM message_events
WHERE kind = ? AND group_id = ? AND event_time >= ? AND id NOT LIKE '%:notice:%'
  AND COALESCE(text, '') != ''
  AND NOT (json_valid(payload) AND COALESCE(json_extract(payload, '$.outbound'), 0) = 1)
ORDER BY event_time DESC
LIMIT ?
`, string(assistant.EventKindGroup), cloud.GroupID, sinceUnix, groupWordCloudScanLimit+1)
	if err != nil {
		return assistant.GroupWordCloud{}, fmt.Errorf("query group word cloud: %w", err)
	}
	defer func() { _ = rows.Close() }()

	speakers := map[string]struct{}{}
	scanned := 0
	for rows.Next() {
		var userID, text string
		if err := rows.Scan(&userID, &text); err != nil {
			return assistant.GroupWordCloud{}, fmt.Errorf("scan group word cloud: %w", err)
		}
		scanned++
		if scanned > groupWordCloudScanLimit {
			cloud.Truncated = true
			break
		}
		userID = strings.TrimSpace(userID)
		if botID != "" && userID == botID {
			continue
		}
		cloud.Messages++
		if userID != "" {
			speakers[userID] = struct{}{}
		}
		counter.Add(text)
	}
	if err := rows.Err(); err != nil {
		return assistant.GroupWordCloud{}, fmt.Errorf("iterate group word cloud: %w", err)
	}
	cloud.Speakers = len(speakers)
	cloud.Words = counter.Top(assistant.GroupWordCloudMaxWords)
	return cloud, nil
}
