// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// EventRangeStats 是一段时间窗内的消息处理情况，给总览页按时间窗展示用。
type EventRangeStats struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// Messages 按消息发生时间计，Handled/Errors 按处理完成时间计。和仪表盘日统计
	// 同一个分法：收到多少条问的是那段时间来了多少消息，回了多少条问的是那段时间
	// 机器人干了多少活，两者本来就不该用同一个时间轴。
	Messages int64 `json:"messages"`
	Handled  int64 `json:"handled"`
	Errors   int64 `json:"errors"`
	// AvgReplyMS 只统计真的回复了、且记到耗时的那些事件；RepliesMeasured 是它的样本数，
	// 为 0 时平均值没有意义，前端据此显示「—」而不是 0。
	AvgReplyMS      int64 `json:"avg_reply_ms"`
	RepliesMeasured int64 `json:"replies_measured"`
}

// inboundHandledPredicate 对应 DescribeEventOutcome 里 handled 为 true 的那组 outcome：
// replied / replied_* 和 error_replied / error_replied_*（回复发出去了，只是内容是错误说明）。
// 不能只匹配 error_replied 全等——error_replied_content_policy 这些也算回复过。
const inboundHandledPredicate = `(outcome LIKE 'replied%' OR outcome LIKE 'error_replied%')`

// EventStatsRange 汇总一段时间窗内的消息量、回复量、错误量和平均回复耗时。
//
// 口径对齐运行时那份内存计数器：错误看 processing_error，它就是 EventRecord.Error
// 落库的那一列（RecordInboundEventAudit），而不是 outcome —— 用 outcome 的话
// error_send_unconfirmed、dropped_outbound_delivery 这些会整类漏掉，和「今日错误」
// 对不上。
func (s *SQLiteStore) EventStatsRange(ctx context.Context, since, until time.Time) (EventRangeStats, error) {
	stats := EventRangeStats{Since: since, Until: until}
	if s == nil || s.db == nil {
		return stats, fmt.Errorf("event stats storage unavailable")
	}
	if !since.Before(until) {
		return stats, fmt.Errorf("invalid stats window")
	}
	defer s.observeStorage(ctx, "EventStatsRange", "read")()

	// event_time 是 Unix 秒，created_at/completed_at 是 Unix 纳秒，同一张表里两种单位。
	sinceSec, untilSec := since.Unix(), until.Unix()
	sinceNanos, untilNanos := since.UnixNano(), until.UnixNano()

	if err := s.eventReader().QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbound_events WHERE event_time >= ? AND event_time < ?
`, sinceSec, untilSec).Scan(&stats.Messages); err != nil {
		return stats, fmt.Errorf("count window messages: %w", err)
	}

	var avg sql.NullFloat64
	if err := s.eventReader().QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN COALESCE(TRIM(processing_error), '') <> '' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0 THEN 1 ELSE 0 END), 0),
  AVG(CASE WHEN `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0 THEN duration_ms END)
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ?
`, sinceNanos, untilNanos).Scan(&stats.Handled, &stats.Errors, &stats.RepliesMeasured, &avg); err != nil {
		return stats, fmt.Errorf("count window outcomes: %w", err)
	}
	if avg.Valid {
		stats.AvgReplyMS = int64(avg.Float64)
	}
	return stats, nil
}
