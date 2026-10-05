package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
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
	if err != nil || got.Calls != 0 || got.Breakdown == nil || len(got.Breakdown) != 0 {
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
	if len(filtered.Usage.Breakdown) != 2 || len(filtered.Groups[0].Usage.Breakdown) != 2 || len(filtered.Groups[0].Purposes["reply"].Breakdown) != 1 {
		t.Fatalf("filtered breakdowns = %+v", filtered)
	}
	var calls, tokens int64
	for _, entry := range filtered.Usage.Breakdown {
		calls += entry.Calls
		tokens += entry.TotalTokens
	}
	if calls != filtered.Usage.Calls || tokens != filtered.Usage.TotalTokens {
		t.Fatalf("filtered breakdown differs from totals: %+v", filtered)
	}
	robot, err := s.LLMUsageReport(ctx, applog.UsageFilter{ProfileID: "a"}, since, until)
	if err != nil || robot.Usage.TotalTokens != 620 {
		t.Fatalf("robot=%+v err=%v", robot, err)
	}
}

func TestLLMUsageEmptyMetadataAndCorruption(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if err := s.AppendLog(ctx, applog.Entry{ID: "legacy", Action: "llm_usage", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []any{nil, "", "  ", "null", "{}"} {
		if _, err := s.db.ExecContext(ctx, `UPDATE app_logs SET metadata = ? WHERE id = 'legacy'`, metadata); err != nil {
			t.Fatal(err)
		}
		got, err := s.LLMUsageSince(ctx, now, now.Add(time.Second))
		if err != nil || got.Calls != 1 || got.TotalTokens != 0 || got.MissingUsageCalls != 1 || len(got.Breakdown) != 1 {
			t.Fatalf("metadata=%#v: %+v, %v", metadata, got, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE app_logs SET metadata = '{broken' WHERE id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LLMUsageSince(ctx, now, now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "invalid usage metadata") {
		t.Fatalf("corrupted metadata must report a read error, got %v", err)
	}
}

func TestLLMUsageBreakdownSurvivesRestartAndMatchesTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	until := time.Date(2026, 10, 5, 12, 0, 0, 500000000, time.UTC)
	since := until.Add(-time.Hour)
	entries := []map[string]any{
		{"purpose": " reply ", "provider": "provider-a", "model": " model-a ", "input_tokens": 100, "output_tokens": 20, "cached_input_tokens": 60},
		{"purpose": "reply", "provider": "provider-a", "model": "model-a", "input_tokens": 10, "output_tokens": 5, "total_tokens": 18},
		{"purpose": "memory_extract", "provider": "provider-a", "model": "model-a", "input_tokens": 30, "output_tokens": 4},
		{"purpose": "reply", "provider": "provider-b", "model": "model-a", "input_tokens": 8, "output_tokens": 2},
		{"purpose": "image_generate", "provider": "provider-a", "model": "image-model", "usage_missing": true},
		{"input_tokens": 2, "output_tokens": 1},
		{}, // Historical calls without reported usage still count.
	}
	for i, metadata := range entries {
		if err := s.AppendLog(ctx, applog.Entry{Action: "llm_usage", CreatedAt: since.Add(time.Duration(i) * time.Minute), Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
	}
	// Exact upper boundary and unrelated logs must not enter any group.
	for _, entry := range []applog.Entry{
		{Action: "llm_usage", CreatedAt: until, Metadata: entries[0]},
		{Action: "agent_tool", CreatedAt: since, Metadata: entries[0]},
	} {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.LLMUsageSince(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LLMUsageSince(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("usage changed after restart: before=%+v after=%+v", before, got)
	}
	want := []applog.UsageBreakdown{
		{Purpose: "reply", Provider: "provider-a", Model: "model-a", Calls: 2, InputTokens: 110, OutputTokens: 25, TotalTokens: 138, CachedInputTokens: 60},
		{Purpose: "memory_extract", Provider: "provider-a", Model: "model-a", Calls: 1, InputTokens: 30, OutputTokens: 4, TotalTokens: 34},
		{Purpose: "reply", Provider: "provider-b", Model: "model-a", Calls: 1, InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
		{Purpose: "unlabeled", Model: "unknown", Calls: 2, InputTokens: 2, OutputTokens: 1, TotalTokens: 3, MissingUsageCalls: 1},
		{Purpose: "image_generate", Provider: "provider-a", Model: "image-model", Calls: 1, MissingUsageCalls: 1},
	}
	if !reflect.DeepEqual(got.Breakdown, want) {
		t.Fatalf("breakdown = %+v, want %+v", got.Breakdown, want)
	}
	if got.Calls != 7 || got.InputTokens != 150 || got.OutputTokens != 32 || got.TotalTokens != 185 || got.CachedInputTokens != 60 || got.MissingUsageCalls != 2 {
		t.Fatalf("totals = %+v", got)
	}
	var sum applog.UsageSummary
	for _, group := range got.Breakdown {
		sum.Calls += group.Calls
		sum.InputTokens += group.InputTokens
		sum.OutputTokens += group.OutputTokens
		sum.TotalTokens += group.TotalTokens
		sum.CachedInputTokens += group.CachedInputTokens
		sum.MissingUsageCalls += group.MissingUsageCalls
	}
	if sum.Calls != got.Calls || sum.InputTokens != got.InputTokens || sum.OutputTokens != got.OutputTokens || sum.TotalTokens != got.TotalTokens || sum.CachedInputTokens != got.CachedInputTokens || sum.MissingUsageCalls != got.MissingUsageCalls {
		t.Fatalf("breakdown sums differ from totals: sum=%+v totals=%+v", sum, got)
	}
	empty, err := s.LLMUsageSince(ctx, until, until.Add(time.Nanosecond))
	if err != nil || len(empty.Breakdown) != 1 || empty.Calls != 1 {
		t.Fatalf("upper boundary call: %+v, %v", empty, err)
	}
}
