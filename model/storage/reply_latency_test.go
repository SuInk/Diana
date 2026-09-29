// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestSummarizeLatencyNearestRank(t *testing.T) {
	values := make([]int64, 0, 100)
	// 倒着放，确认汇总前会排序。
	for value := int64(100); value >= 1; value-- {
		values = append(values, value)
	}
	got := SummarizeLatency(values)
	want := LatencyDistribution{Samples: 100, AvgMS: 51, P50MS: 50, P90MS: 90, P99MS: 99, MinMS: 1, MaxMS: 100}
	if got != want {
		t.Fatalf("SummarizeLatency = %+v, want %+v", got, want)
	}
	if values[0] != 100 {
		t.Fatal("SummarizeLatency must not reorder the caller's slice")
	}
}

func TestSummarizeLatencyLongTailDoesNotMoveMedian(t *testing.T) {
	// 九条 2 秒、一条 200 秒：平均被拖到 21.8 秒，中位数仍是 2 秒，P99 抓得住那条慢的。
	values := []int64{2000, 2000, 2000, 2000, 2000, 2000, 2000, 2000, 2000, 200000}
	got := SummarizeLatency(values)
	if got.AvgMS != 21800 || got.P50MS != 2000 || got.P90MS != 2000 || got.P99MS != 200000 {
		t.Fatalf("SummarizeLatency = %+v", got)
	}
}

func TestSummarizeLatencyEmptyAndSingle(t *testing.T) {
	if got := SummarizeLatency(nil); got != (LatencyDistribution{}) {
		t.Fatalf("empty = %+v, want zero", got)
	}
	got := SummarizeLatency([]int64{1234})
	if got.Samples != 1 || got.P50MS != 1234 || got.P99MS != 1234 || got.AvgMS != 1234 {
		t.Fatalf("single = %+v", got)
	}
}

func ms(value int64) *int64 { return &value }

func TestSummarizeReplyLatencyWindowPhasesAndExtremes(t *testing.T) {
	until := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	since := until.Add(-time.Hour)
	at := func(minutesAgo int) time.Time { return until.Add(-time.Duration(minutesAgo) * time.Minute) }
	samples := []ReplyLatencySample{
		{EventID: "a", CompletedAt: at(10), TotalMS: 1000, WaitMS: ms(100), TTFTMS: ms(300), ModelMS: ms(800)},
		{EventID: "b", CompletedAt: at(20), TotalMS: 5000, WaitMS: ms(200), ModelMS: ms(3000), ToolMS: ms(1500)},
		{EventID: "c", CompletedAt: at(30), TotalMS: 3000},
		// 窗口起点算在内，终点不算。
		{EventID: "edge-in", CompletedAt: since, TotalMS: 2000},
		{EventID: "edge-out", CompletedAt: until, TotalMS: 99000},
		{EventID: "old", CompletedAt: at(90), TotalMS: 88000},
	}
	summary := SummarizeReplyLatency(samples, since, until, 2)
	if summary.Total.Samples != 4 || summary.Total.MaxMS != 5000 {
		t.Fatalf("Total = %+v, want 4 samples max 5000", summary.Total)
	}
	// 量不到的阶段不参与：等待 2 条、首 token 1 条、模型 2 条、工具 1 条。
	if summary.Wait.Samples != 2 || summary.Wait.AvgMS != 150 {
		t.Fatalf("Wait = %+v", summary.Wait)
	}
	if summary.TTFT.Samples != 1 || summary.TTFT.P50MS != 300 {
		t.Fatalf("TTFT = %+v", summary.TTFT)
	}
	if summary.Model.Samples != 2 || summary.Tool.Samples != 1 || summary.Tool.MaxMS != 1500 {
		t.Fatalf("Model = %+v, Tool = %+v", summary.Model, summary.Tool)
	}
	if ids := sampleIDs(summary.Slowest); ids != "b,c" {
		t.Fatalf("Slowest = %s, want b,c", ids)
	}
	if ids := sampleIDs(summary.Fastest); ids != "a,edge-in" {
		t.Fatalf("Fastest = %s, want a,edge-in", ids)
	}

	// 样本少于两倍条数时，最快那边只取最慢没取走的，同一条不出现两次。
	few := SummarizeReplyLatency(samples[:3], since, until, 2)
	if ids := sampleIDs(few.Slowest) + "|" + sampleIDs(few.Fastest); ids != "b,c|a" {
		t.Fatalf("extremes = %s, want b,c|a", ids)
	}
	if none := SummarizeReplyLatency(samples, since, until, 0); none.Slowest != nil || none.Fastest != nil {
		t.Fatal("extremes = 0 must not list samples")
	}
}

func sampleIDs(samples []ReplyLatencySample) string {
	out := ""
	for index, sample := range samples {
		if index > 0 {
			out += ","
		}
		out += sample.EventID
	}
	return out
}

// insertLatencyEvent 写一行入队时刻为 enqueuedAt 的群消息，还没处理完。
func insertLatencyEvent(t *testing.T, s *SQLiteStore, id, messageID, payload string, enqueuedAt time.Time) {
	t.Helper()
	_, err := s.db.Exec(`
INSERT INTO inbound_events (id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload, available_at, status, outcome, created_at, updated_at)
VALUES (?, 'session', 'group', 'bot-a', '10001', '20002', ?, ?, ?, 0, 'done', 'replied', ?, ?)
`, id, messageID, enqueuedAt.Unix(), payload, enqueuedAt.UnixNano(), enqueuedAt.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
}

func completeLatencyEvent(t *testing.T, s *SQLiteStore, id string, completedAt time.Time) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE inbound_events SET completed_at = ? WHERE id = ?`, completedAt.UnixNano(), id); err != nil {
		t.Fatal(err)
	}
}

func TestReplyLatencySamplesAttachesMeasuredPhases(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "latency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	until := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	// 第一条：入队 1.5 秒后开始回复，整轮 10 秒。
	enqueued := until.Add(-30 * time.Minute)
	turnStart := enqueued.Add(1500 * time.Millisecond)
	completed := turnStart.Add(10 * time.Second)
	insertLatencyEvent(t, s, "evt-1", "30001", `{}`, enqueued)
	record := assistant.EventRecord{
		At: turnStart, Kind: assistant.EventKindGroup, GroupID: "10001", UserID: "20002", MessageID: "30001",
		Decision: "replied", Duration: 10000,
	}
	if err := s.RecordInboundEventAudit(ctx, record); err != nil {
		t.Fatal(err)
	}
	completeLatencyEvent(t, s, "evt-1", completed)

	logAt := func(action, target string, at time.Time, meta map[string]any) {
		t.Helper()
		if err := s.AppendLog(ctx, AppLogEntry{Action: action, Target: target, Metadata: meta, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	// 回复轮次之前的路由判断调用不算进这一轮的模型耗时。
	logAt("llm_usage", "30001", turnStart.Add(-500*time.Millisecond), map[string]any{"duration_ms": 400, "ttft_ms": 50})
	// 轮次里：一次非流式调用先开始，一次流式调用后开始。首 token 取流式那次，不拿非流式当 0。
	logAt("llm_usage", "30001", turnStart.Add(3*time.Second), map[string]any{"duration_ms": 2500})
	logAt("llm_usage", "30001", turnStart.Add(9*time.Second), map[string]any{"duration_ms": 4000, "ttft_ms": 700})
	logAt("agent_run", "30001", turnStart.Add(9500*time.Millisecond), map[string]any{"phase": "completed", "tools_duration_ms": 1800})
	logAt("agent_run", "30001", turnStart.Add(100*time.Millisecond), map[string]any{"phase": "started"})

	// 第二条：主人手动重试过（入队是两小时前），没有模型日志，Agent 收尾记录是升级前的老格式。
	oldEnqueued := until.Add(-150 * time.Minute)
	retryStart := until.Add(-10 * time.Minute)
	insertLatencyEvent(t, s, "evt-2", "30002", `{"manual_retry":true}`, oldEnqueued)
	record.At, record.MessageID, record.Duration = retryStart, "30002", 4000
	if err := s.RecordInboundEventAudit(ctx, record); err != nil {
		t.Fatal(err)
	}
	completeLatencyEvent(t, s, "evt-2", retryStart.Add(4*time.Second))
	logAt("agent_run", "30002", retryStart.Add(3*time.Second), map[string]any{"phase": "completed", "tools_executed": 2})

	samples, err := s.ReplyLatencySamples(ctx, until.Add(-time.Hour), until, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	first := samples[0]
	if first.EventID != "evt-1" || first.TotalMS != 10000 {
		t.Fatalf("first = %+v", first)
	}
	if first.WaitMS == nil || *first.WaitMS != 1500 {
		t.Fatalf("WaitMS = %v, want 1500", first.WaitMS)
	}
	if first.ModelMS == nil || *first.ModelMS != 6500 || first.ModelCalls != 2 {
		t.Fatalf("ModelMS = %v over %d calls, want 6500 over 2", first.ModelMS, first.ModelCalls)
	}
	if first.TTFTMS == nil || *first.TTFTMS != 700 {
		t.Fatalf("TTFTMS = %v, want 700", first.TTFTMS)
	}
	if first.ToolMS == nil || *first.ToolMS != 1800 {
		t.Fatalf("ToolMS = %v, want 1800", first.ToolMS)
	}

	second := samples[1]
	if second.WaitMS != nil {
		t.Fatalf("manual retry WaitMS = %d, want unmeasured", *second.WaitMS)
	}
	if second.ModelMS != nil || second.TTFTMS != nil || second.ToolMS != nil {
		t.Fatalf("second phases = %+v, want all unmeasured", second)
	}

	// 按机器人筛：别的机器人看不到这两条。
	other, err := s.ReplyLatencySamples(ctx, until.Add(-time.Hour), until, "bot-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("bot-b samples = %d, want 0", len(other))
	}
}
