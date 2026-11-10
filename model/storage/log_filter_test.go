// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
)

// 浏览器页按动作和机器人取操作记录：别的动作、别的机器人、没带机器人 ID 的都不该混进来。
func TestListLogsFiltersByActionAndProfile(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	entries := []AppLogEntry{
		{ID: "a-open", Kind: LogKindOperation, Action: "browser_action", Metadata: map[string]any{"profile_id": "bot-a"}, CreatedAt: now},
		{ID: "a-start", Kind: LogKindOperation, Action: "browser_box_start", Metadata: map[string]any{"profile_id": "bot-a"}, CreatedAt: now.Add(time.Second)},
		{ID: "b-open", Kind: LogKindError, Action: "browser_action", Metadata: map[string]any{"profile_id": "bot-b"}, CreatedAt: now},
		{ID: "a-other", Kind: LogKindOperation, Action: "agent_tool", Metadata: map[string]any{"profile_id": "bot-a"}, CreatedAt: now},
		{ID: "no-profile", Kind: LogKindOperation, Action: "browser_action", CreatedAt: now},
	}
	for _, entry := range entries {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListLogs(ctx, AppLogFilter{Actions: []string{"browser_action", "browser_box_start"}, ProfileID: "bot-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a-start" || got[1].ID != "a-open" {
		t.Fatalf("按动作和机器人筛选不对：%+v", got)
	}
	all, err := s.ListLogs(ctx, AppLogFilter{Actions: []string{"browser_action"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("只按动作筛时应拿到所有机器人和两种级别的记录，实际 %d 条", len(all))
	}
}

// 日志页的「全部」是操作加错误，调试追踪不混进来。
func TestListLogsFiltersByKinds(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	for _, entry := range []AppLogEntry{
		{ID: "op", Kind: LogKindOperation, CreatedAt: now},
		{ID: "err", Kind: LogKindError, CreatedAt: now.Add(time.Second)},
		{ID: "debug", Kind: LogKindDebug, CreatedAt: now.Add(2 * time.Second)},
	} {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListLogs(ctx, AppLogFilter{Kinds: []AppLogKind{LogKindOperation, LogKindError}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "err" || got[1].ID != "op" {
		t.Fatalf("全部应只含操作和错误并按时间倒序：%+v", got)
	}
}

// 日志页按游标往下翻：同一时刻的多条靠 ID 接着翻，不重复也不漏。
func TestListLogsPaginatesWithCursor(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	for index, id := range []string{"a", "b", "c", "d", "e"} {
		at := base.Add(time.Duration(index) * time.Second)
		if id == "c" {
			at = base.Add(3 * time.Second) // 和 d 同一时刻
		}
		if err := s.AppendLog(ctx, AppLogEntry{ID: id, Kind: LogKindOperation, Action: "x", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	filter := AppLogFilter{Limit: 2}
	for page := 0; page < 5; page++ {
		got, err := s.ListLogs(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			break
		}
		for _, entry := range got {
			seen = append(seen, entry.ID)
		}
		last := got[len(got)-1]
		filter.BeforeTime, filter.BeforeID = last.CreatedAt, last.ID
	}
	if want := "e,d,c,b,a"; strings.Join(seen, ",") != want {
		t.Fatalf("翻页顺序 = %v，想要 %s", seen, want)
	}
}

// 搜索在库里做：内容、元数据、按昵称反查出的账号、按群名反查出的群号都算命中；
// LIKE 通配符要转义；排除的动作不出现。
func TestListLogsSearchAndExclude(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	for _, entry := range []AppLogEntry{
		{ID: "msg", Kind: LogKindOperation, Action: "x", Message: "仓库检查完成", CreatedAt: now},
		{ID: "meta", Kind: LogKindOperation, Action: "x", Message: "m", Metadata: map[string]any{"model": "gpt-6-sol"}, CreatedAt: now},
		{ID: "actor", Kind: LogKindOperation, Action: "x", Message: "m", Actor: "qq:30001", CreatedAt: now},
		{ID: "group", Kind: LogKindOperation, Action: "x", Message: "m", Metadata: map[string]any{"group_id": "40001"}, CreatedAt: now},
		{ID: "percent", Kind: LogKindOperation, Action: "x", Message: "进度 100%", CreatedAt: now},
		{ID: "usage", Kind: LogKindOperation, Action: "llm_usage", Message: "仓库检查完成", CreatedAt: now},
	} {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(filter AppLogFilter) string {
		t.Helper()
		got, err := s.ListLogs(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, entry := range got {
			out = append(out, entry.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := ids(AppLogFilter{Query: "仓库", ExcludeActions: []string{"llm_usage"}}); got != "msg" {
		t.Fatalf("按内容搜并排除用量记录 = %s", got)
	}
	if got := ids(AppLogFilter{Query: "gpt-6"}); got != "meta" {
		t.Fatalf("按元数据搜 = %s", got)
	}
	if got := ids(AppLogFilter{Query: "阿树", QueryActors: []string{"30001"}, QueryGroupIDs: []string{"40001"}}); got != "actor,group" {
		t.Fatalf("按昵称、群名反查 = %s", got)
	}
	if got := ids(AppLogFilter{Query: "0%"}); got != "percent" {
		t.Fatalf("百分号应按字面匹配 = %s", got)
	}
}

// 失败的请求只进 FailedCalls，不改成功调用次数和 token。
func TestLLMUsageReportCountsFailedCalls(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, entry := range []AppLogEntry{
		{Kind: LogKindOperation, Action: "llm_usage", Metadata: map[string]any{"model": "m1", "purpose": "chat_reply", "input_tokens": 10, "output_tokens": 5}, CreatedAt: now.Add(-time.Minute)},
		{Kind: LogKindError, Action: "llm_call_failed", Metadata: map[string]any{"model": "m0", "purpose": "chat_reply"}, CreatedAt: now.Add(-time.Minute)},
		{Kind: LogKindError, Action: "llm_call_failed", Metadata: map[string]any{"model": "m0", "purpose": "chat_reply"}, CreatedAt: now.Add(-time.Minute)},
	} {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := s.LLMUsageSince(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Calls != 1 || summary.FailedCalls != 2 || summary.TotalTokens != 15 {
		t.Fatalf("summary = %+v", summary)
	}
	var failedOnM0 int64
	for _, row := range summary.Breakdown {
		if row.Model == "m0" {
			failedOnM0 = row.FailedCalls
			if row.Calls != 0 {
				t.Fatalf("失败的模型不该算成功调用：%+v", row)
			}
		}
	}
	if failedOnM0 != 2 {
		t.Fatalf("按模型的失败次数 = %d", failedOnM0)
	}
}

// 按日期区间和元数据（模型、用途）筛；拼进 JSON 路径的键只放行普通标识符。
func TestListLogsFiltersByTimeAndMetadata(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, entry := range []AppLogEntry{
		{ID: "old", Kind: LogKindOperation, Action: "llm_usage", Metadata: map[string]any{"model": "m1", "purpose": "chat_reply"}, CreatedAt: day.Add(-time.Hour)},
		{ID: "in-m1", Kind: LogKindOperation, Action: "llm_usage", Metadata: map[string]any{"model": "m1", "purpose": "chat_reply"}, CreatedAt: day.Add(time.Hour)},
		{ID: "in-m2", Kind: LogKindOperation, Action: "llm_usage", Metadata: map[string]any{"model": "m2", "purpose": "send_audit"}, CreatedAt: day.Add(2 * time.Hour)},
		{ID: "late", Kind: LogKindOperation, Action: "llm_usage", Metadata: map[string]any{"model": "m1", "purpose": "chat_reply"}, CreatedAt: day.Add(25 * time.Hour)},
	} {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListLogs(ctx, AppLogFilter{Since: day, Until: day.Add(24 * time.Hour), Metadata: map[string]string{"model": "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "in-m1" {
		t.Fatalf("日期加模型筛选 = %+v", got)
	}
	if got, err = s.ListLogs(ctx, AppLogFilter{Metadata: map[string]string{"purpose": "send_audit"}}); err != nil || len(got) != 1 || got[0].ID != "in-m2" {
		t.Fatalf("按用途筛选 = %+v, %v", got, err)
	}
	if _, err := s.ListLogs(ctx, AppLogFilter{Metadata: map[string]string{"model') OR 1=1 --": "x"}}); err == nil {
		t.Fatal("带引号的元数据键应被拒绝")
	}
}
