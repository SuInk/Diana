package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func TestLLMUsageRollingWindow(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	until := time.Date(2026, 9, 9, 12, 30, 0, 500000000, time.UTC)
	since := until.Add(-24 * time.Hour)
	for i, item := range []struct {
		at     time.Time
		action string
	}{
		{since.Add(-time.Nanosecond), "llm_usage"},
		{since, "assistant.llm_usage"},
		{since.Add(time.Hour), "chatbot.llm_usage"},
		{until.Add(-time.Nanosecond), "llm_usage"},
		{until, "llm_usage"},
		{until.Add(time.Nanosecond), "llm_usage"},
		{since.Add(time.Hour), "assistant.agent_tool"},
	} {
		meta := map[string]any{"input_tokens": 100, "output_tokens": 20, "cached_input_tokens": 60}
		if i == 2 {
			meta["total_tokens"] = 130
		}
		if err := s.AppendLog(ctx, applog.Entry{Action: item.action, CreatedAt: item.at, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
	}
	rerunLogActionNameMigration(t, s)
	got, err := s.LLMUsageSince(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if got.Calls != 3 || got.InputTokens != 300 || got.OutputTokens != 60 || got.TotalTokens != 370 || got.CachedInputTokens != 180 {
		t.Fatalf("incorrect totals: %+v", got)
	}
	// A whole-second boundary must still include fractional timestamps in that second.
	got, err = s.LLMUsageSince(ctx, since.Truncate(time.Second), since.Truncate(time.Second).Add(time.Second))
	if err != nil || got.Calls != 2 {
		t.Fatalf("fractional timestamps: %+v, %v", got, err)
	}
	got, err = s.LLMUsageSince(ctx, until.Add(time.Hour), until.Add(2*time.Hour))
	if err != nil || got.Calls != 0 {
		t.Fatalf("empty window: %+v, %v", got, err)
	}
}

// 按群统计要能把别的群、别的机器人和没有群归属的调用都排除掉；一次问全部群的
// 那条路径口径必须和逐群查完全一致，不然控制台画出来的进度条和真正拦人的数对不上。
func TestGroupLLMUsageSplitsByGroupAndProfile(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "group-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	until := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	since := until.Add(-5 * time.Hour)
	for _, item := range []struct {
		at    time.Time
		meta  map[string]any
		label string
	}{
		{since.Add(time.Minute), map[string]any{"group_id": "111", "profile_id": "bot-a", "total_tokens": 900}, "命中"},
		{since.Add(2 * time.Minute), map[string]any{"group_id": "111", "profile_id": "bot-a", "input_tokens": 60, "output_tokens": 40}, "上游没报 total 也算一次"},
		{since.Add(3 * time.Minute), map[string]any{"group_id": "222", "profile_id": "bot-a", "total_tokens": 500}, "另一个群"},
		{since.Add(4 * time.Minute), map[string]any{"group_id": "111", "profile_id": "bot-b", "total_tokens": 7000}, "同群另一台机器人"},
		{since.Add(5 * time.Minute), map[string]any{"profile_id": "bot-a", "total_tokens": 3000}, "私聊/后台，没有群归属"},
		{since.Add(-time.Minute), map[string]any{"group_id": "111", "profile_id": "bot-a", "total_tokens": 4000}, "窗口之前"},
	} {
		if err := s.AppendLog(ctx, applog.Entry{Action: "llm_usage", CreatedAt: item.at, Metadata: item.meta}); err != nil {
			t.Fatalf("%s: %v", item.label, err)
		}
	}
	got, err := s.GroupLLMUsageSince(ctx, "bot-a", "111", since, until)
	if err != nil {
		t.Fatal(err)
	}
	if got.Calls != 2 {
		t.Fatalf("单群统计不对: %+v", got)
	}
	bulk, err := s.GroupLLMUsageSinceByProfile(ctx, "bot-a", since, until)
	if err != nil {
		t.Fatal(err)
	}
	if bulk["111"] != got {
		t.Fatalf("两条路径口径不一致: %+v vs %+v", bulk["111"], got)
	}
	if bulk["222"].Calls != 1 {
		t.Fatalf("另一个群统计不对: %+v", bulk["222"])
	}
	// 没有群归属的调用不该被归到某个群名下，也不该自成一条。
	if len(bulk) != 2 {
		t.Fatalf("多出了不该有的分组: %+v", bulk)
	}
}

func TestLLMUsageReportSeparatesRobotsPlatformsPurposesAndUnknownHistory(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	since := time.Date(2026, 10, 3, 0, 0, 0, 500000000, time.UTC)
	until := since.Add(time.Hour)
	entries := []struct {
		at   time.Time
		meta map[string]any
	}{
		{since.Add(-time.Nanosecond), map[string]any{"total_tokens": 99999}},
		{since, map[string]any{"profile_id": "a", "platform": "onebot", "group_id": "g", "purpose": "reply", "input_tokens": 100, "output_tokens": 20, "cached_input_tokens": 60}},
		{until.Add(-time.Nanosecond), map[string]any{"profile_id": "a", "platform": "onebot", "group_id": "g", "purpose": "memory", "usage_missing": true}},
		{since.Add(time.Minute), map[string]any{"profile_id": "b", "platform": "onebot", "group_id": "g", "total_tokens": 500}},
		{since.Add(time.Minute), map[string]any{"profile_id": "a", "platform": "telegram", "group_id": "g", "total_tokens": 300}},
		{since.Add(time.Minute), map[string]any{"profile_id": "a", "total_tokens": 200}},
		{since.Add(time.Minute), map[string]any{"total_tokens": 1000}},
		{until, map[string]any{"total_tokens": 99999}},
	}
	for _, e := range entries {
		if err := s.AppendLog(ctx, applog.Entry{Action: "llm_usage", CreatedAt: e.at, Metadata: e.meta}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.LLMUsageReport(ctx, applog.UsageFilter{}, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if report.Usage.Calls != 6 || report.Usage.TotalTokens != 2120 || report.Usage.CachedInputTokens != 60 || report.Usage.MissingUsageCalls != 1 || len(report.Groups) != 3 {
		t.Fatalf("report=%+v", report)
	}
	if report.Groups[0].ProfileID != "b" || report.Groups[1].Platform != "telegram" {
		t.Fatalf("ranking=%+v", report.Groups)
	}
	filtered, err := s.LLMUsageReport(ctx, applog.UsageFilter{ProfileID: "a", Platform: "onebot", GroupID: "g"}, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Usage.Calls != 2 || filtered.Usage.TotalTokens != 120 || len(filtered.Groups) != 1 || filtered.Groups[0].Purposes["memory"].MissingUsageCalls != 1 || filtered.Groups[0].Purposes["reply"].CachedInputTokens != 60 {
		t.Fatalf("filtered=%+v", filtered)
	}
	robot, err := s.LLMUsageReport(ctx, applog.UsageFilter{ProfileID: "a"}, since, until)
	if err != nil || robot.Usage.TotalTokens != 620 {
		t.Fatalf("robot=%+v err=%v", robot, err)
	}
}
