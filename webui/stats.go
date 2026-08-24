// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// statsStatusProvider 是统计接口对运行时的最小依赖，便于测试注入。
type statsStatusProvider interface {
	Status() assistant.RuntimeStatus
}

// hourBucket 按小时聚合事件量；hourUnix 是该小时起点的 Unix 秒。
type hourBucket struct {
	HourUnix int64 `json:"hour_unix"`
	Total    int64 `json:"total"`
	Handled  int64 `json:"handled"`
	Errors   int64 `json:"errors"`
}

// statsCounters 是一份计数。控制台可以按机器人切换，所以同一批事件要同时记进
// 「全部机器人」和「这台机器人」两份里，字段抽出来省得写两遍。
type statsCounters struct {
	total       int64
	handled     int64
	errors      int64
	byKind      map[string]int64
	buckets     map[int64]*hourBucket
	lastEventAt time.Time
	durTotalMS  int64
	durCount    int64
}

func newStatsCounters() *statsCounters {
	return &statsCounters{byKind: map[string]int64{}, buckets: map[int64]*hourBucket{}}
}

func (c *statsCounters) observe(event assistant.EventRecord, at time.Time, hour int64) {
	c.total++
	c.byKind[string(event.Kind)]++
	bucket, ok := c.buckets[hour]
	if !ok {
		bucket = &hourBucket{HourUnix: hour}
		c.buckets[hour] = bucket
	}
	bucket.Total++
	if event.Handled {
		c.handled++
		bucket.Handled++
	}
	if event.Error != "" {
		c.errors++
		bucket.Errors++
	}
	if event.Handled && event.Duration > 0 {
		c.durTotalMS += event.Duration
		c.durCount++
	}
	if at.After(c.lastEventAt) {
		c.lastEventAt = at
	}
}

// trim 只保留最近 48 小时的分桶，防止长期运行内存无界增长。
func (c *statsCounters) trim(cutoff int64) {
	for key := range c.buckets {
		if key < cutoff {
			delete(c.buckets, key)
		}
	}
}

func (c *statsCounters) restore(stats storage.DashboardEventStats) {
	c.total = stats.TotalEvents
	c.handled = stats.HandledEvents
	c.errors = stats.ErrorEvents
	c.byKind = make(map[string]int64, len(stats.ByKind))
	for kind, count := range stats.ByKind {
		c.byKind[kind] = count
	}
	c.buckets = make(map[int64]*hourBucket, len(stats.Hourly))
	for _, restored := range stats.Hourly {
		bucket := restored
		c.buckets[restored.HourUnix] = &hourBucket{
			HourUnix: bucket.HourUnix,
			Total:    bucket.Total,
			Handled:  bucket.Handled,
			Errors:   bucket.Errors,
		}
	}
	c.lastEventAt = stats.LastEventAt
	c.durTotalMS = stats.DurationTotalMS
	c.durCount = stats.DurationCount
}

// StatsCollector 聚合运行时事件计数；进程启动时可从 SQLite 恢复基线，
// 之后配置重载继续复用同一个实例。
type StatsCollector struct {
	mu        sync.Mutex
	startedAt time.Time
	all       *statsCounters
	byProfile map[string]*statsCounters
	now       func() time.Time
}

// RestoreDurableBaseline restores counters collected before this process
// started. Call it before attaching Observe as the runtime event listener.
func (s *StatsCollector) RestoreDurableBaseline(stats storage.DashboardEventStats) {
	s.RestoreDurableBaselines(map[string]storage.DashboardEventStats{"": stats})
}

// RestoreDurableBaselines 按机器人恢复基线。键 "" 是全部机器人的合计。
func (s *StatsCollector) RestoreDurableBaselines(baselines map[string]storage.DashboardEventStats) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.all = newStatsCounters()
	s.byProfile = map[string]*statsCounters{}
	for profileID, stats := range baselines {
		counters := s.all
		if profileID != "" {
			counters = newStatsCounters()
			s.byProfile[profileID] = counters
		}
		counters.restore(stats)
	}
}

// NewStatsCollector 创建 StatsCollector。
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{
		startedAt: time.Now(),
		all:       newStatsCounters(),
		byProfile: map[string]*statsCounters{},
		now:       time.Now,
	}
}

// Observe 记录一条运行时事件，供 Runtime.SetEventListener 注入。
func (s *StatsCollector) Observe(event assistant.EventRecord) {
	at := event.At
	if at.IsZero() {
		at = s.now()
	}
	hour := at.Truncate(time.Hour).Unix()

	s.mu.Lock()
	defer s.mu.Unlock()
	targets := []*statsCounters{s.all}
	if profileID := strings.TrimSpace(event.ProfileID); profileID != "" {
		counters, ok := s.byProfile[profileID]
		if !ok {
			counters = newStatsCounters()
			s.byProfile[profileID] = counters
		}
		targets = append(targets, counters)
	}
	cutoff := s.now().Add(-48 * time.Hour).Truncate(time.Hour).Unix()
	for _, counters := range targets {
		counters.observe(event, at, hour)
		counters.trim(cutoff)
	}
}

// StatsBotSummary 是 Dashboard 需要的机器人状态摘要。
type StatsBotSummary struct {
	Running        bool   `json:"running"`
	Connected      bool   `json:"connected"`
	SelfID         string `json:"self_id,omitempty"`
	ActiveWorkers  int    `json:"active_workers"`
	PluginsEnabled int    `json:"plugins_enabled"`
	PluginsTotal   int    `json:"plugins_total"`
	LastError      string `json:"last_error,omitempty"`
	BridgeEnabled  bool   `json:"bridge_enabled"`
	BridgeOK       bool   `json:"bridge_connected"`
}

// StatsProfileCounters 是单台机器人的那部分计数。字段名和 StatsSnapshot 里对应
// 的一致：前端按控制台选中的机器人覆盖上去就行，不用另写一套读法。运行时长、
// 服务器占用这类进程级指标不在里面——它们本来就只有一份。
type StatsProfileCounters struct {
	TotalEvents   int64            `json:"total_events"`
	HandledEvents int64            `json:"handled_events"`
	ErrorEvents   int64            `json:"error_events"`
	TodayEvents   int64            `json:"today_events"`
	TodayHandled  int64            `json:"today_handled"`
	TodayErrors   int64            `json:"today_errors"`
	ByKind        map[string]int64 `json:"by_kind"`
	Hourly        []hourBucket     `json:"hourly"`
	AvgReplyMS    int64            `json:"avg_reply_ms"`
	LastEventAt   *time.Time       `json:"last_event_at,omitempty"`
}

// StatsSnapshot 是 GET /api/stats 的响应结构。
type StatsSnapshot struct {
	StartedAt     time.Time                       `json:"started_at"`
	UptimeSeconds int64                           `json:"uptime_seconds"`
	TotalEvents   int64                           `json:"total_events"`
	HandledEvents int64                           `json:"handled_events"`
	ErrorEvents   int64                           `json:"error_events"`
	TodayEvents   int64                           `json:"today_events"`
	TodayHandled  int64                           `json:"today_handled"`
	TodayErrors   int64                           `json:"today_errors"`
	ByKind        map[string]int64                `json:"by_kind"`
	Hourly        []hourBucket                    `json:"hourly"`
	AvgReplyMS    int64                           `json:"avg_reply_ms"`
	LastEventAt   *time.Time                      `json:"last_event_at,omitempty"`
	Bot           StatsBotSummary                 `json:"bot"`
	Server        storage.DashboardServerStats    `json:"server"`
	ByProfile     map[string]StatsProfileCounters `json:"by_profile,omitempty"`
}

// counters 抽出快照里按机器人分的那部分。
func (s StatsSnapshot) counters() StatsProfileCounters {
	return StatsProfileCounters{
		TotalEvents:   s.TotalEvents,
		HandledEvents: s.HandledEvents,
		ErrorEvents:   s.ErrorEvents,
		TodayEvents:   s.TodayEvents,
		TodayHandled:  s.TodayHandled,
		TodayErrors:   s.TodayErrors,
		ByKind:        s.ByKind,
		Hourly:        s.Hourly,
		AvgReplyMS:    s.AvgReplyMS,
		LastEventAt:   s.LastEventAt,
	}
}

// SnapshotWithProfiles 返回合计快照，并在 ByProfile 里附上每台机器人各自的计数。
//
// SSE 是一条广播通道，推给所有订阅者的是同一份数据；把每台的计数一起带上，前端
// 切换机器人时就不用重连或重新拉一次，也不会出现某个页签把别人的实时数字盖掉。
func (s *StatsCollector) SnapshotWithProfiles() StatsSnapshot {
	snapshot := s.SnapshotForProfile("")
	for _, profileID := range s.profileIDs() {
		if snapshot.ByProfile == nil {
			snapshot.ByProfile = map[string]StatsProfileCounters{}
		}
		snapshot.ByProfile[profileID] = s.SnapshotForProfile(profileID).counters()
	}
	return snapshot
}

// profileIDs 列出已经记过事件的机器人。
func (s *StatsCollector) profileIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.byProfile))
	for id := range s.byProfile {
		ids = append(ids, id)
	}
	return ids
}

// Snapshot 汇总全部机器人的统计数据。
func (s *StatsCollector) Snapshot() StatsSnapshot {
	return s.SnapshotForProfile("")
}

// SnapshotForProfile 汇总某台机器人的统计数据，botProfileID 留空表示全部机器人；
// hourly 覆盖最近 24 小时并按时间升序补零。运行时长这类进程级指标不分机器人，
// 本来就只有一份。
func (s *StatsCollector) SnapshotForProfile(botProfileID string) StatsSnapshot {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	counters := s.all
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		// 这台机器人还没有任何事件时给一份空计数，而不是退回合计：切过去看到别人
		// 的数字比看到 0 更容易让人误判。
		if scoped, ok := s.byProfile[botProfileID]; ok {
			counters = scoped
		} else {
			counters = newStatsCounters()
		}
	}

	snapshot := StatsSnapshot{
		StartedAt:     s.startedAt,
		UptimeSeconds: int64(now.Sub(s.startedAt).Seconds()),
		TotalEvents:   counters.total,
		HandledEvents: counters.handled,
		ErrorEvents:   counters.errors,
		ByKind:        map[string]int64{},
	}
	for kind, count := range counters.byKind {
		snapshot.ByKind[kind] = count
	}
	if counters.durCount > 0 {
		snapshot.AvgReplyMS = counters.durTotalMS / counters.durCount
	}
	if !counters.lastEventAt.IsZero() {
		last := counters.lastEventAt
		snapshot.LastEventAt = &last
	}

	currentHour := now.Truncate(time.Hour).Unix()
	snapshot.Hourly = make([]hourBucket, 0, 24)
	for i := 23; i >= 0; i-- {
		hour := currentHour - int64(i)*3600
		if bucket, ok := counters.buckets[hour]; ok {
			snapshot.Hourly = append(snapshot.Hourly, *bucket)
		} else {
			snapshot.Hourly = append(snapshot.Hourly, hourBucket{HourUnix: hour})
		}
	}

	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	for hour, bucket := range counters.buckets {
		if hour >= dayStart {
			snapshot.TodayEvents += bucket.Total
			snapshot.TodayHandled += bucket.Handled
			snapshot.TodayErrors += bucket.Errors
		}
	}
	return snapshot
}

// StatsHandler 提供 Dashboard 用的聚合统计接口。
type StatsHandler struct {
	collector   *StatsCollector
	runtime     statsStatusProvider
	storagePath string
}

// NewStatsHandler 创建 StatsHandler 实例。
func NewStatsHandler(collector *StatsCollector, runtime statsStatusProvider, storagePaths ...string) *StatsHandler {
	storagePath := ""
	if len(storagePaths) > 0 {
		storagePath = storagePaths[0]
	}
	return &StatsHandler{collector: collector, runtime: runtime, storagePath: storagePath}
}

// Register 注册当前模块的路由或能力。
func (h *StatsHandler) Register(router gin.IRouter) {
	router.GET("/api/stats", h.stats)
}

// stats 返回运行统计和机器人状态摘要。
func (h *StatsHandler) stats(c *gin.Context) {
	snapshot := h.collector.SnapshotWithProfiles()
	snapshot.Server = cachedDashboardServerStats(time.Now(), h.storagePath)
	if h.runtime != nil {
		snapshot.Bot = summarizeBotStatus(h.runtime.Status())
	}
	c.JSON(http.StatusOK, snapshot)
}

// summarizeBotStatus 把完整运行时状态压缩成 Dashboard 摘要。
func summarizeBotStatus(status assistant.RuntimeStatus) StatsBotSummary {
	summary := StatsBotSummary{
		Running:       status.Running,
		Connected:     status.Channel.Connected,
		SelfID:        status.Channel.SelfID,
		ActiveWorkers: status.ActiveWorkers,
		LastError:     status.LastError,
		BridgeEnabled: status.NoneBotBridge.Enabled,
		BridgeOK:      status.NoneBotBridge.Connected,
	}
	summary.PluginsTotal = len(status.Plugins)
	for _, plugin := range status.Plugins {
		if plugin.Installed && plugin.Enabled {
			summary.PluginsEnabled++
		}
	}
	return summary
}
