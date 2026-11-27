// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type DashboardServerStats struct {
	CollectedAt          time.Time `json:"collected_at"`
	Hostname             string    `json:"hostname,omitempty"`
	OS                   string    `json:"os"`
	Arch                 string    `json:"arch"`
	ProcessID            int       `json:"process_id"`
	ProcessUptimeSeconds int64     `json:"process_uptime_seconds,omitempty"`
	CPUModel             string    `json:"cpu_model,omitempty"`
	CPUCores             int       `json:"cpu_cores"`
	CPUUsagePercent      float64   `json:"cpu_usage_percent,omitempty"`
	// 空闲时 Diana 的 CPU 占用就是 0，omitempty 会把这个真实读数整条抹掉，
	// 前端分不清「没测到」和「刚好是 0」。采集失败另有
	// ProcessMetricsUnavailable 表示。
	ProcessCPUPercent  float64 `json:"process_cpu_percent"`
	MemoryTotalBytes   uint64  `json:"memory_total_bytes,omitempty"`
	MemoryUsedBytes    uint64  `json:"memory_used_bytes,omitempty"`
	MemoryUsagePercent float64 `json:"memory_usage_percent,omitempty"`
	ProcessMemoryBytes uint64  `json:"process_memory_bytes,omitempty"`
	// ProcessStorageBytes 是 Diana 数据目录的体积，也就是它自己占掉的磁盘。
	// 首次采样还没跑完时是 0，下一次就有值。
	ProcessStorageBytes       uint64  `json:"process_storage_bytes"`
	StoragePath               string  `json:"storage_path,omitempty"`
	StorageTotalBytes         uint64  `json:"storage_total_bytes,omitempty"`
	StorageUsedBytes          uint64  `json:"storage_used_bytes,omitempty"`
	StorageAvailableBytes     uint64  `json:"storage_available_bytes,omitempty"`
	StorageUsagePercent       float64 `json:"storage_usage_percent,omitempty"`
	GoHeapAllocBytes          uint64  `json:"go_heap_alloc_bytes,omitempty"`
	GoHeapSystemBytes         uint64  `json:"go_heap_system_bytes,omitempty"`
	GoRoutines                int     `json:"go_routines"`
	RuntimeVersion            string  `json:"runtime_version,omitempty"`
	MetricsUnavailableReason  string  `json:"metrics_unavailable_reason,omitempty"`
	ProcessMetricsUnavailable string  `json:"process_metrics_unavailable,omitempty"`
	StorageMetricsUnavailable string  `json:"storage_metrics_unavailable,omitempty"`
}

// DashboardEventStats is the durable baseline used by the live Dashboard
// collector after a process restart. Only terminal inbound events are included;
// queued work is counted by the live collector after it actually runs.
type DashboardEventStats struct {
	TotalEvents     int64
	HandledEvents   int64
	ErrorEvents     int64
	ByKind          map[string]int64
	Hourly          []DashboardEventStatsBucket
	LastEventAt     time.Time
	DurationTotalMS int64
	DurationCount   int64
}

type DashboardEventStatsBucket struct {
	HourUnix int64
	Total    int64
	Handled  int64
	Errors   int64
}

// dashboardEventBaselineSQL 读出重建控制台基线要用的全部历史行。两段都要把整张
// 表读一遍，逐行回表读带着整段 payload 的宽行，2.7 GB 的库冷缓存要 4–16 秒，所以
// 启动时放到后台跑，见 BeginDashboardEventStatsSnapshot。
const dashboardEventBaselineSQL = `
SELECT kind, event_time, outcome, created_at, completed_at, profile_id, failed
FROM (
  SELECT kind, event_time, COALESCE(outcome, '') AS outcome,
         created_at, COALESCE(completed_at, 0) AS completed_at,
         TRIM(COALESCE(profile_id, '')) AS profile_id,
         ` + inboundFailedPredicate + ` AS failed
  FROM inbound_events
  WHERE status = 'done' AND COALESCE(outcome, '') != 'ignored_stale'

  UNION ALL

  SELECT m.kind, m.event_time, '', 0, 0, TRIM(COALESCE(m.profile_id, '')), 0
  FROM message_events AS m
  LEFT JOIN inbound_events AS i ON i.id = m.id
  WHERE i.id IS NULL
    AND m.kind IN ('group', 'private')
    AND NULLIF(TRIM(m.message_id), '') IS NOT NULL
)
`

// DashboardEventStatsSnapshot rebuilds the live collector baseline from
// deduplicated SQLite records. It runs once during process startup so normal
// config reloads keep using the same in-memory collector.
func (s *SQLiteStore) DashboardEventStatsSnapshot(ctx context.Context, now time.Time) (DashboardEventStats, error) {
	baselines, err := s.DashboardEventStatsSnapshotByProfile(ctx, now)
	if err != nil {
		return DashboardEventStats{}, err
	}
	return baselines[""], nil
}

// DashboardEventStatsSnapshotByProfile 按机器人拆开基线。键 "" 是全部机器人的
// 合计，其余键是配置档 ID：控制台切到哪台，恢复出来的历史数字也得跟着切，
// 否则重启之后那台机器人会顶着别人的历史量。
func (s *SQLiteStore) DashboardEventStatsSnapshotByProfile(ctx context.Context, now time.Time) (map[string]DashboardEventStats, error) {
	defer s.observeStorage(ctx, "DashboardEventStatsSnapshotByProfile", "read")()
	if s == nil || s.db == nil {
		return map[string]DashboardEventStats{"": {ByKind: map[string]int64{}}}, nil
	}
	return scanDashboardEventBaselines(ctx, s.eventReader(), now)
}

// BeginDashboardEventStatsSnapshot 先在只读池上钉住一个快照，再把真正费时的读取
// 交给返回的函数，调用方可以在后台跑它。
//
// 这条查询要把 inbound_events 和 message_events 各读一遍，生产上 3–16 秒，原来在
// 启动路径上同步等它，机器人和 WebUI 都要等它读完才起来。挪到后台后，基线与运行中
// 新收的事件不能重也不能漏：这里先开读事务、读一页把快照定在「现在」，之后再启动
// 机器人。WAL 下快照一旦建立就不会再看到之后提交的写入，所以基线只含启动前已经
// 完成的事件，启动后的事件只由实时计数器记，两边相加正好各算一次。
//
// 返回的函数必须调用恰好一次，它负责结束读事务。内存库和自定义 DSN 没有独立的
// 只读池，读事务会占住唯一的写连接，所以这时直接同步读完，返回的函数只交出结果。
func (s *SQLiteStore) BeginDashboardEventStatsSnapshot(ctx context.Context, now time.Time) (func() (map[string]DashboardEventStats, error), error) {
	if s == nil || s.db == nil || s.readDB == nil {
		baselines, err := s.DashboardEventStatsSnapshotByProfile(ctx, now)
		return func() (map[string]DashboardEventStats, error) { return baselines, err }, nil
	}
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin dashboard event baseline: %w", err)
	}
	// BEGIN 本身不建快照，第一次读库才建。读一页就够，结果不要紧。
	var pinned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM inbound_events LIMIT 1)`).Scan(&pinned); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("pin dashboard event baseline snapshot: %w", err)
	}
	return func() (map[string]DashboardEventStats, error) {
		defer func() { _ = tx.Rollback() }()
		defer s.observeStorage(ctx, "DashboardEventStatsSnapshotByProfile", "read")()
		return scanDashboardEventBaselines(ctx, tx, now)
	}, nil
}

// dashboardQueryer 是 *sql.DB 和 *sql.Tx 共有的那一个方法。
type dashboardQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanDashboardEventBaselines(ctx context.Context, db dashboardQueryer, now time.Time) (map[string]DashboardEventStats, error) {
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := db.QueryContext(ctx, dashboardEventBaselineSQL)
	if err != nil {
		return nil, fmt.Errorf("query dashboard event baseline: %w", err)
	}
	defer func() { _ = rows.Close() }()

	currentHour := now.Truncate(time.Hour).Unix()
	cutoffHour := currentHour - 47*int64(time.Hour/time.Second)
	accumulators := map[string]*dashboardEventAccumulator{"": newDashboardEventAccumulator()}
	for rows.Next() {
		var kind, outcome, profileID string
		var failed bool
		var eventTime, createdAt, completedAt int64
		if err := rows.Scan(&kind, &eventTime, &outcome, &createdAt, &completedAt, &profileID, &failed); err != nil {
			return nil, fmt.Errorf("scan dashboard event baseline: %w", err)
		}

		targets := []*dashboardEventAccumulator{accumulators[""]}
		if profileID = strings.TrimSpace(profileID); profileID != "" {
			acc := accumulators[profileID]
			if acc == nil {
				acc = newDashboardEventAccumulator()
				accumulators[profileID] = acc
			}
			targets = append(targets, acc)
		}
		handled := dashboardOutcomeHandled(outcome)
		at := time.Unix(eventTime, 0).In(now.Location())
		hour := at.Truncate(time.Hour).Unix()
		inWindow := hour >= cutoffHour && hour <= currentHour
		for _, acc := range targets {
			acc.observe(kind, at, hour, inWindow, handled, failed, createdAt, completedAt)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard event baseline: %w", err)
	}

	baselines := make(map[string]DashboardEventStats, len(accumulators))
	for key, acc := range accumulators {
		baselines[key] = acc.snapshot()
	}
	return baselines, nil
}

// dashboardEventAccumulator 把一行基线记录同时累加到「全部」和「某台机器人」两
// 份计数里，省得为每种作用域各写一遍同样的加法。
type dashboardEventAccumulator struct {
	stats   DashboardEventStats
	buckets map[int64]*DashboardEventStatsBucket
}

func newDashboardEventAccumulator() *dashboardEventAccumulator {
	return &dashboardEventAccumulator{
		stats:   DashboardEventStats{ByKind: map[string]int64{}},
		buckets: map[int64]*DashboardEventStatsBucket{},
	}
}

func (a *dashboardEventAccumulator) observe(kind string, at time.Time, hour int64, inWindow bool, handled bool, failed bool, createdAt int64, completedAt int64) {
	a.stats.TotalEvents++
	a.stats.ByKind[kind]++
	if handled {
		a.stats.HandledEvents++
		if completedAt > createdAt && createdAt > 0 {
			a.stats.DurationTotalMS += (completedAt - createdAt) / int64(time.Millisecond)
			a.stats.DurationCount++
		}
	}
	if failed {
		a.stats.ErrorEvents++
	}
	if at.After(a.stats.LastEventAt) {
		a.stats.LastEventAt = at
	}
	if !inWindow {
		return
	}
	bucket := a.buckets[hour]
	if bucket == nil {
		bucket = &DashboardEventStatsBucket{HourUnix: hour}
		a.buckets[hour] = bucket
	}
	bucket.Total++
	if handled {
		bucket.Handled++
	}
	if failed {
		bucket.Errors++
	}
}

func (a *dashboardEventAccumulator) snapshot() DashboardEventStats {
	stats := a.stats
	stats.Hourly = make([]DashboardEventStatsBucket, 0, len(a.buckets))
	for _, bucket := range a.buckets {
		stats.Hourly = append(stats.Hourly, *bucket)
	}
	return stats
}

func dashboardOutcomeHandled(outcome string) bool {
	outcome = strings.TrimSpace(outcome)
	return outcome == "replied" || outcome == "error_replied" || strings.HasPrefix(outcome, "replied_")
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0
		}
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	default:
		return 0
	}
}

// dashboardProfileScope 拼出机器人作用域条件。message_events 与 inbound_events
// 的列名相同，两张表共用这一段。
func dashboardProfileScope(botProfileID string) (string, []any) {
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		return " AND COALESCE(profile_id, '') = ?", []any{botProfileID}
	}
	return "", nil
}
