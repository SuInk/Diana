package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type fakeStatusProvider struct {
	status assistant.RuntimeStatus
}

func (f *fakeStatusProvider) Status() assistant.RuntimeStatus {
	return f.status
}

// TestStatsCollectorObserveAndSnapshot 验证对应功能场景。
func TestStatsCollectorObserveAndSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 30, 0, 0, time.Local)
	collector := NewStatsCollector()
	collector.now = func() time.Time { return now }
	collector.startedAt = now.Add(-90 * time.Second)

	collector.Observe(assistant.EventRecord{
		At:       now.Add(-10 * time.Minute),
		Kind:     assistant.EventKindGroup,
		Handled:  true,
		Duration: 1200,
	})
	collector.Observe(assistant.EventRecord{
		At:      now.Add(-5 * time.Minute),
		Kind:    assistant.EventKindPrivate,
		Handled: true, Duration: 800,
	})
	collector.Observe(assistant.EventRecord{
		At:    now.Add(-2 * time.Hour),
		Kind:  assistant.EventKindGroup,
		Error: "llm timeout",
	})

	snapshot := collector.Snapshot()
	if snapshot.TotalEvents != 3 {
		t.Fatalf("TotalEvents = %d, want 3", snapshot.TotalEvents)
	}
	if snapshot.HandledEvents != 2 {
		t.Fatalf("HandledEvents = %d, want 2", snapshot.HandledEvents)
	}
	if snapshot.ErrorEvents != 1 {
		t.Fatalf("ErrorEvents = %d, want 1", snapshot.ErrorEvents)
	}
	if snapshot.AvgReplyMS != 1000 {
		t.Fatalf("AvgReplyMS = %d, want 1000", snapshot.AvgReplyMS)
	}
	if snapshot.ByKind["group"] != 2 || snapshot.ByKind["private"] != 1 {
		t.Fatalf("ByKind = %v", snapshot.ByKind)
	}
	if len(snapshot.Hourly) != 24 {
		t.Fatalf("len(Hourly) = %d, want 24", len(snapshot.Hourly))
	}
	last := snapshot.Hourly[len(snapshot.Hourly)-1]
	if last.Total != 2 || last.Handled != 2 {
		t.Fatalf("current hour bucket = %+v, want total 2 handled 2", last)
	}
	prev2 := snapshot.Hourly[len(snapshot.Hourly)-3]
	if prev2.Total != 1 || prev2.Errors != 1 {
		t.Fatalf("bucket 2h ago = %+v, want total 1 errors 1", prev2)
	}
	if snapshot.UptimeSeconds != 90 {
		t.Fatalf("UptimeSeconds = %d, want 90", snapshot.UptimeSeconds)
	}
	// 三条事件都发生在今天（-2h 也在当天 13:30）。
	if snapshot.TodayEvents != 3 {
		t.Fatalf("TodayEvents = %d, want 3", snapshot.TodayEvents)
	}
}

// TestStatsCollectorPrunesOldBuckets 验证对应功能场景。
func TestStatsCollectorPrunesOldBuckets(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.Local)
	collector := NewStatsCollector()
	collector.now = func() time.Time { return now }

	collector.Observe(assistant.EventRecord{At: now.Add(-72 * time.Hour), Kind: assistant.EventKindGroup})
	collector.Observe(assistant.EventRecord{At: now, Kind: assistant.EventKindGroup, Handled: true})

	collector.mu.Lock()
	buckets := len(collector.buckets)
	collector.mu.Unlock()
	if buckets != 1 {
		t.Fatalf("buckets = %d, want 1 (72h-old bucket pruned)", buckets)
	}
	// 总计数不受分桶清理影响。
	snapshot := collector.Snapshot()
	if snapshot.TotalEvents != 2 {
		t.Fatalf("TotalEvents = %d, want 2", snapshot.TotalEvents)
	}
}

func TestStatsCollectorRestoresDurableBaselineAndContinuesIncrementally(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 30, 0, 0, time.Local)
	collector := NewStatsCollector()
	collector.now = func() time.Time { return now }
	collector.startedAt = now.Add(-90 * time.Second)
	collector.RestoreDurableBaseline(storage.DashboardEventStats{
		TotalEvents:     4,
		HandledEvents:   2,
		ErrorEvents:     1,
		ByKind:          map[string]int64{"group": 3, "private": 1},
		LastEventAt:     now.Add(-time.Hour),
		DurationTotalMS: 4000,
		DurationCount:   2,
		Hourly: []storage.DashboardEventStatsBucket{
			{HourUnix: now.Add(-time.Hour).Truncate(time.Hour).Unix(), Total: 4, Handled: 2, Errors: 1},
		},
	})

	collector.Observe(assistant.EventRecord{
		At:       now,
		Kind:     assistant.EventKindGroup,
		Handled:  true,
		Duration: 2000,
	})

	snapshot := collector.Snapshot()
	if snapshot.TotalEvents != 5 || snapshot.HandledEvents != 3 || snapshot.ErrorEvents != 1 {
		t.Fatalf("totals = total:%d handled:%d errors:%d, want 5/3/1", snapshot.TotalEvents, snapshot.HandledEvents, snapshot.ErrorEvents)
	}
	if snapshot.ByKind["group"] != 4 || snapshot.ByKind["private"] != 1 {
		t.Fatalf("by kind = %#v, want group:4 private:1", snapshot.ByKind)
	}
	if snapshot.AvgReplyMS != 2000 {
		t.Fatalf("AvgReplyMS = %d, want 2000", snapshot.AvgReplyMS)
	}
	if snapshot.UptimeSeconds != 90 || !snapshot.StartedAt.Equal(now.Add(-90*time.Second)) {
		t.Fatalf("process timing changed during restore: started=%s uptime=%d", snapshot.StartedAt, snapshot.UptimeSeconds)
	}
	if snapshot.LastEventAt == nil || !snapshot.LastEventAt.Equal(now) {
		t.Fatalf("LastEventAt = %v, want %s", snapshot.LastEventAt, now)
	}
}

// TestStatsHandlerReturnsSnapshotWithBotSummary 验证对应功能场景。
func TestStatsHandlerReturnsSnapshotWithBotSummary(t *testing.T) {
	collector := NewStatsCollector()
	collector.Observe(assistant.EventRecord{At: time.Now(), Kind: assistant.EventKindGroup, Handled: true})

	provider := &fakeStatusProvider{status: assistant.RuntimeStatus{
		Running:       true,
		Channel:       assistant.ChannelStatus{Connected: true, SelfID: "10001"},
		ActiveWorkers: 2,
		Plugins: []assistant.PluginState{
			{Installed: true, Enabled: true},
			{Installed: true, Enabled: false},
		},
	}}

	handler := NewStatsHandler(collector, provider)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var snapshot StatsSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if snapshot.TotalEvents != 1 {
		t.Fatalf("TotalEvents = %d, want 1", snapshot.TotalEvents)
	}
	if !snapshot.Bot.Running || !snapshot.Bot.Connected || snapshot.Bot.SelfID != "10001" {
		t.Fatalf("Bot summary = %+v", snapshot.Bot)
	}
	if snapshot.Bot.PluginsEnabled != 1 || snapshot.Bot.PluginsTotal != 2 {
		t.Fatalf("Bot plugins = %+v", snapshot.Bot)
	}
}
