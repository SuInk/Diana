// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestInboundActivityBucketsGroupsByQuarterHourProfileAndGroup(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	until := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	insert := func(id, profile, group string, at time.Time) {
		t.Helper()
		if _, err := s.db.Exec(`
INSERT INTO inbound_events (id, session, kind, event_time, payload, available_at, status, profile_id, group_id, created_at, updated_at)
VALUES (?, 'session', 'group', ?, '{}', 0, 'done', ?, ?, ?, ?)
`, id, at.Unix(), profile, group, at.UnixNano(), at.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	insert("a1", "bot-a", "g1", until.Add(-50*time.Minute)) // 11:10 → 11:00 桶
	insert("a2", "bot-a", "g2", until.Add(-46*time.Minute)) // 11:14 → 11:00 桶
	insert("a3", "bot-a", "g1", until.Add(-40*time.Minute)) // 11:20 → 11:15 桶
	insert("b1", "bot-b", "g1", until.Add(-50*time.Minute))
	insert("old", "bot-a", "g1", until.Add(-48*time.Hour)) // 窗口外
	insert("edge", "bot-a", "g1", until)                   // 右开区间，不算

	since := until.Add(-24 * time.Hour)
	all, err := s.InboundActivityBuckets(ctx, since, until, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Count != 3 || all[1].Count != 1 {
		t.Fatalf("all buckets = %+v", all)
	}
	if !all[0].Start.Equal(time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC)) || !all[1].Start.Equal(time.Date(2026, 10, 7, 11, 15, 0, 0, time.UTC)) {
		t.Fatalf("bucket starts = %v, %v", all[0].Start, all[1].Start)
	}

	scoped, err := s.InboundActivityBuckets(ctx, since, until, "bot-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 || scoped[0].Count != 2 || scoped[1].Count != 1 {
		t.Fatalf("bot-a buckets = %+v", scoped)
	}

	group, err := s.InboundActivityBuckets(ctx, since, until, "bot-a", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 2 || group[0].Count != 1 || group[1].Count != 1 {
		t.Fatalf("bot-a g1 buckets = %+v", group)
	}

	// 按 UTC+8 归日：11:10 UTC 是当天 19:10；48 小时前那条落在两天前。
	days, err := s.InboundActivityDays(ctx, until.Add(-72*time.Hour), until, 8*3600, "bot-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0] != (InboundActivityDayCount{Date: "2026-10-05", Count: 1}) || days[1] != (InboundActivityDayCount{Date: "2026-10-07", Count: 3}) {
		t.Fatalf("bot-a days = %+v", days)
	}
	// 本地 00:30 的消息要归到本地当天，而不是 UTC 的前一天。
	insert("midnight", "bot-c", "g1", time.Date(2026, 10, 6, 16, 30, 0, 0, time.UTC))
	days, err = s.InboundActivityDays(ctx, until.Add(-72*time.Hour), until, 8*3600, "bot-c", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Date != "2026-10-07" {
		t.Fatalf("bot-c days = %+v", days)
	}

	if _, err := s.InboundActivityBuckets(ctx, until, until, "", ""); err == nil {
		t.Fatal("empty window should be rejected")
	}
}
