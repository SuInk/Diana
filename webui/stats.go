package webui

import (
	"net/http"
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

// StatsCollector 聚合运行时事件计数；进程启动时可从 SQLite 恢复基线，
// 之后配置重载继续复用同一个实例。
type StatsCollector struct {
	mu          sync.Mutex
	startedAt   time.Time
	total       int64
	handled     int64
	errors      int64
	byKind      map[string]int64
	buckets     map[int64]*hourBucket
	lastEventAt time.Time
	durTotalMS  int64
	durCount    int64
	now         func() time.Time
}

// RestoreDurableBaseline restores counters collected before this process
// started. Call it before attaching Observe as the runtime event listener.
func (s *StatsCollector) RestoreDurableBaseline(stats storage.DashboardEventStats) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.total = stats.TotalEvents
	s.handled = stats.HandledEvents
	s.errors = stats.ErrorEvents
	s.byKind = make(map[string]int64, len(stats.ByKind))
	for kind, count := range stats.ByKind {
		s.byKind[kind] = count
	}
	s.buckets = make(map[int64]*hourBucket, len(stats.Hourly))
	for _, restored := range stats.Hourly {
		bucket := restored
		s.buckets[restored.HourUnix] = &hourBucket{
			HourUnix: bucket.HourUnix,
			Total:    bucket.Total,
			Handled:  bucket.Handled,
			Errors:   bucket.Errors,
		}
	}
	s.lastEventAt = stats.LastEventAt
	s.durTotalMS = stats.DurationTotalMS
	s.durCount = stats.DurationCount
}

// NewStatsCollector 创建 StatsCollector。
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{
		startedAt: time.Now(),
		byKind:    map[string]int64{},
		buckets:   map[int64]*hourBucket{},
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
	s.total++
	s.byKind[string(event.Kind)]++
	bucket, ok := s.buckets[hour]
	if !ok {
		bucket = &hourBucket{HourUnix: hour}
		s.buckets[hour] = bucket
	}
	bucket.Total++
	if event.Handled {
		s.handled++
		bucket.Handled++
	}
	if event.Error != "" {
		s.errors++
		bucket.Errors++
	}
	if event.Handled && event.Duration > 0 {
		s.durTotalMS += event.Duration
		s.durCount++
	}
	if at.After(s.lastEventAt) {
		s.lastEventAt = at
	}
	// 只保留最近 48 小时的分桶，防止长期运行内存无界增长。
	cutoff := s.now().Add(-48 * time.Hour).Truncate(time.Hour).Unix()
	for key := range s.buckets {
		if key < cutoff {
			delete(s.buckets, key)
		}
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

// StatsSnapshot 是 GET /api/stats 的响应结构。
type StatsSnapshot struct {
	StartedAt     time.Time                    `json:"started_at"`
	UptimeSeconds int64                        `json:"uptime_seconds"`
	TotalEvents   int64                        `json:"total_events"`
	HandledEvents int64                        `json:"handled_events"`
	ErrorEvents   int64                        `json:"error_events"`
	TodayEvents   int64                        `json:"today_events"`
	TodayHandled  int64                        `json:"today_handled"`
	TodayErrors   int64                        `json:"today_errors"`
	ByKind        map[string]int64             `json:"by_kind"`
	Hourly        []hourBucket                 `json:"hourly"`
	AvgReplyMS    int64                        `json:"avg_reply_ms"`
	LastEventAt   *time.Time                   `json:"last_event_at,omitempty"`
	Bot           StatsBotSummary              `json:"bot"`
	Server        storage.DashboardServerStats `json:"server"`
}

// Snapshot 汇总当前统计数据；hourly 覆盖最近 24 小时并按时间升序补零。
func (s *StatsCollector) Snapshot() StatsSnapshot {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := StatsSnapshot{
		StartedAt:     s.startedAt,
		UptimeSeconds: int64(now.Sub(s.startedAt).Seconds()),
		TotalEvents:   s.total,
		HandledEvents: s.handled,
		ErrorEvents:   s.errors,
		ByKind:        map[string]int64{},
	}
	for kind, count := range s.byKind {
		snapshot.ByKind[kind] = count
	}
	if s.durCount > 0 {
		snapshot.AvgReplyMS = s.durTotalMS / s.durCount
	}
	if !s.lastEventAt.IsZero() {
		last := s.lastEventAt
		snapshot.LastEventAt = &last
	}

	currentHour := now.Truncate(time.Hour).Unix()
	snapshot.Hourly = make([]hourBucket, 0, 24)
	for i := 23; i >= 0; i-- {
		hour := currentHour - int64(i)*3600
		if bucket, ok := s.buckets[hour]; ok {
			snapshot.Hourly = append(snapshot.Hourly, *bucket)
		} else {
			snapshot.Hourly = append(snapshot.Hourly, hourBucket{HourUnix: hour})
		}
	}

	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	for hour, bucket := range s.buckets {
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
	snapshot := h.collector.Snapshot()
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
