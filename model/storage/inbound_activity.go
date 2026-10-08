// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// InboundActivityBucket 是一段 15 分钟里收到的消息数，Start 是这段的起点。
type InboundActivityBucket struct {
	Start time.Time
	Count int64
}

// inboundActivityBucketSeconds 是按时间聚合的粒度。按 15 分钟而不是整点分桶：
// 换算成本地时间时，+5:30、+5:45 这类时区的整点落在 UTC 的半点或刻钟上，按 UTC 整点
// 分桶会把一个本地小时劈成两半。
const inboundActivityBucketSeconds = 15 * 60

// InboundActivityBuckets 按 15 分钟汇总事件时间落在 [since, until) 里的收到消息数，
// 只返回有消息的桶。profileID 非空时只看这台机器人，groupID 非空时只看这个群。
//
// 口径和总览「收到消息」一致：按 event_time 计，不看处理结果。聚合在库里做完，
// 一年最多三万多行，不把每条消息读回来。
func (s *SQLiteStore) InboundActivityBuckets(ctx context.Context, since, until time.Time, profileID, groupID string) ([]InboundActivityBucket, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("inbound activity storage unavailable")
	}
	if !since.Before(until) {
		return nil, fmt.Errorf("invalid activity window")
	}
	defer s.observeStorage(ctx, "InboundActivityBuckets", "read")()

	args := []any{inboundActivityBucketSeconds, since.Unix(), until.Unix()}
	conditions := ""
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		conditions += " AND profile_id = ?"
		args = append(args, profileID)
	}
	if groupID = strings.TrimSpace(groupID); groupID != "" {
		conditions += " AND group_id = ?"
		args = append(args, groupID)
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT event_time / ? AS bucket, COUNT(*)
FROM inbound_events
WHERE event_time >= ? AND event_time < ?`+conditions+`
GROUP BY bucket
ORDER BY bucket`, args...)
	if err != nil {
		return nil, fmt.Errorf("query inbound activity: %w", err)
	}
	defer rows.Close()
	var buckets []InboundActivityBucket
	for rows.Next() {
		var bucket, count int64
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, fmt.Errorf("scan inbound activity: %w", err)
		}
		buckets = append(buckets, InboundActivityBucket{Start: time.Unix(bucket*inboundActivityBucketSeconds, 0).UTC(), Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbound activity: %w", err)
	}
	return buckets, nil
}

// InboundActivityDayCount 是本地某一天收到的消息数，Date 形如 2006-01-02。
type InboundActivityDayCount struct {
	Date  string
	Count int64
}

// InboundActivityDays 按本地日期汇总事件时间落在 [since, until) 里的收到消息数，只返回
// 有消息的日子。utcOffsetSeconds 是本地时区相对 UTC 的偏移：按天聚合直接在库里做，
// 几年也只有一两千行；代价是夏令时切换前后那段按此刻的偏移归日，最多错开一小时。
func (s *SQLiteStore) InboundActivityDays(ctx context.Context, since, until time.Time, utcOffsetSeconds int, profileID, groupID string) ([]InboundActivityDayCount, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("inbound activity storage unavailable")
	}
	if !since.Before(until) {
		return nil, fmt.Errorf("invalid activity window")
	}
	defer s.observeStorage(ctx, "InboundActivityDays", "read")()

	args := []any{utcOffsetSeconds, since.Unix(), until.Unix()}
	conditions := ""
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		conditions += " AND profile_id = ?"
		args = append(args, profileID)
	}
	if groupID = strings.TrimSpace(groupID); groupID != "" {
		conditions += " AND group_id = ?"
		args = append(args, groupID)
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT (event_time + ?) / 86400 AS day, COUNT(*)
FROM inbound_events
WHERE event_time >= ? AND event_time < ?`+conditions+`
GROUP BY day
ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("query inbound activity days: %w", err)
	}
	defer rows.Close()
	var days []InboundActivityDayCount
	for rows.Next() {
		var day, count int64
		if err := rows.Scan(&day, &count); err != nil {
			return nil, fmt.Errorf("scan inbound activity days: %w", err)
		}
		days = append(days, InboundActivityDayCount{Date: time.Unix(day*86400, 0).UTC().Format(time.DateOnly), Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbound activity days: %w", err)
	}
	return days, nil
}
