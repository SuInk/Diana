// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
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
