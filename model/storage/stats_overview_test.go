// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func TestDashboardOverviewAggregatesLongSpanTrendsGroupsAndErrors(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "overview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	shanghai := time.FixedZone("UTC+8", 8*3600)
	until := time.Date(2026, 10, 10, 12, 0, 0, 0, shanghai)
	since := until.AddDate(0, 0, -7)
	prevAt := since.Add(-24 * time.Hour)

	insertEvent := func(id, profileID, groupID, userID, msgID, status, outcome, procErr string, at time.Time, durMS int64) {
		t.Helper()
		completed := int64(0)
		if status == "done" {
			completed = at.Add(time.Duration(durMS) * time.Millisecond).UnixNano()
		}
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO inbound_events (
  id, session, kind, profile_id, group_id, user_id, message_id, payload,
  event_time, status, available_at, outcome, processing_error, duration_ms, created_at, updated_at, completed_at
) VALUES (?, ?, 'group', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "group:"+groupID, profileID, groupID, userID, msgID, `{"group_name":"测试群"}`,
			at.Unix(), status, at.UnixNano(), outcome, procErr, durMS, at.UnixNano(), at.UnixNano(), completed,
		); err != nil {
			t.Fatalf("insert event %s: %v", id, err)
		}
	}

	// 前一周期数据（用于环比对照）
	insertEvent("ev-prev", "bot-a", "10001", "u1", "m-prev", "done", "replied", "", prevAt, 1500)
	if err := s.AppendLog(ctx, applog.Entry{
		Action:    "llm_usage",
		CreatedAt: prevAt,
		Metadata:  map[string]any{"profile_id": "bot-a", "group_id": "10001", "model": "gpt-6", "purpose": "reply", "input_tokens": 200, "output_tokens": 50, "total_tokens": 250},
	}); err != nil {
		t.Fatal(err)
	}

	// 当前 7 天窗口数据
	ev1At := until.Add(-48 * time.Hour)
	ev2At := until.Add(-3 * time.Hour)
	insertEvent("ev-ok", "bot-a", "10001", "u1", "m-1", "done", "replied", "", ev1At, 2000)
	insertEvent("ev-err", "bot-a", "10001", "u2", "m-2", "done", "processing_error", "upstream 502 bad gateway", ev2At, 0)
	insertEvent("ev-other-bot", "bot-b", "20002", "u3", "m-3", "done", "replied", "", ev2At, 1000)

	for _, entry := range []applog.Entry{
		{
			Action: "llm_usage", CreatedAt: ev1At,
			Metadata: map[string]any{
				"profile_id": "bot-a", "platform": "onebot", "group_id": "10001",
				"provider": "openai_compatible", "model": "gpt-6", "purpose": "reply",
				"input_tokens": 1000, "output_tokens": 200, "cached_input_tokens": 600, "total_tokens": 1200,
				"duration_ms": 2000, "ttft_ms": 250,
			},
		},
		{
			Kind: applog.KindError, Action: "llm_call_failed", Message: "模型「gpt-6」调用失败", Detail: "429 rate limit", CreatedAt: ev2At,
			Metadata: map[string]any{
				"profile_id": "bot-a", "group_id": "10001", "provider": "openai_compatible",
				"model": "gpt-6", "purpose": "reply", "message_id": "m-2", "recovered": true,
			},
		},
		{
			Kind: applog.KindError, Action: "plugin_exec", Message: "插件执行异常", Detail: "timeout", CreatedAt: ev2At.Add(time.Minute),
			Metadata: map[string]any{"profile_id": "bot-a", "group_id": "10001"},
		},
	} {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.DashboardOverview(ctx, DashboardOverviewQuery{
		Since:            since,
		Until:            until,
		ProfileID:        "bot-a",
		Granularity:      "day",
		UTCOffsetSeconds: 8 * 3600,
		ErrorLimit:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Granularity != "day" || got.Current.Messages != 2 || got.Current.Handled != 1 || got.Current.Errors != 1 {
		t.Fatalf("unexpected current event summary: %+v", got.Current)
	}
	if got.Current.ActiveGroups != 1 || got.Current.ActiveUsers != 2 || got.Current.AppErrorLogs != 1 {
		t.Fatalf("unexpected active/error counts: %+v", got.Current)
	}
	if got.Current.Usage.TotalTokens != 1200 || got.Current.Usage.FailedCalls != 1 || got.Current.Usage.RecoveredCalls != 1 || got.Current.Usage.OutputTokensPerSecond != 100 {
		t.Fatalf("unexpected usage summary: %+v", got.Current.Usage)
	}
	if got.Previous.Messages != 1 || got.Previous.Handled != 1 || got.Previous.TotalTokens != 250 || got.Previous.RecordedCalls != 1 {
		t.Fatalf("unexpected previous period summary: %+v", got.Previous)
	}
	if len(got.Groups) != 1 || got.Groups[0].GroupID != "10001" || got.Groups[0].GroupName != "测试群" || got.Groups[0].TotalTokens != 1200 || got.Groups[0].Errors != 1 {
		t.Fatalf("unexpected group ranking: %+v", got.Groups)
	}
	if got.Errors.EventErrors != 1 || got.Errors.LLMFailedCalls != 1 || got.Errors.AppErrorLogs != 1 || len(got.Errors.Categories) != 3 || len(got.Errors.Recent) != 3 {
		t.Fatalf("unexpected errors overview: %+v", got.Errors)
	}
	if len(got.Trend) == 0 {
		t.Fatal("expected non-empty trend buckets")
	}
}
