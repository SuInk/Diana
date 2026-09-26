// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testTaskClock(year int, month time.Month, day, hour, minute int) taskClock {
	location := time.FixedZone("CST", 8*3600)
	return taskClock{Reference: time.Date(year, month, day, hour, minute, 0, 0, location), Location: location}
}

func TestResolveTaskDateTime(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	date := func(year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, location)
	}
	// 2026-09-30 19:00，周三。
	clock := testTaskClock(2026, time.September, 30, 19, 0)
	cases := []struct {
		name string
		date string
		time string
		want time.Time
	}{
		{"今天", "today", "22:00", date(2026, time.September, 30, 22, 0)},
		{"明天跨月", "tomorrow", "08:30", date(2026, time.October, 1, 8, 30)},
		{"后天跨月", "day_after_tomorrow", "22:00", date(2026, time.October, 2, 22, 0)},
		{"中文明天", "明天", "9:05", date(2026, time.October, 1, 9, 5)},
		{"完整日期", "2026-10-08", "10:00", date(2026, time.October, 8, 10, 0)},
		{"月日未过", "12-25", "09:00", date(2026, time.December, 25, 9, 0)},
		{"月日已过取明年", "09-01", "09:00", date(2027, time.September, 1, 9, 0)},
		{"几号已过取下个月", "28", "09:00", date(2026, time.October, 28, 9, 0)},
		{"今天几号但时刻未过", "30", "21:00", date(2026, time.September, 30, 21, 0)},
		{"今天几号且时刻已过取下个月", "30", "18:00", date(2026, time.October, 30, 18, 0)},
		{"只给时刻未过", "", "20:00", date(2026, time.September, 30, 20, 0)},
		{"只给时刻已过取明天", "", "07:00", date(2026, time.October, 1, 7, 0)},
		{"几号带号字", "28号", "09:00", date(2026, time.October, 28, 9, 0)},
	}
	for _, tc := range cases {
		got, err := resolveTaskDateTime(map[string]any{"date": tc.date, "time": tc.time}, clock, true)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !got.At.Equal(tc.want) {
			t.Fatalf("%s: got %s, want %s", tc.name, got.At, tc.want)
		}
		if !strings.Contains(got.Note, tc.want.Format("2006-01-02")) {
			t.Fatalf("%s: note %q should name the date", tc.name, got.Note)
		}
	}
}

// 按自然日严格算：凌晨一点说「明天」就是日历上的明天，不当成今天白天。
func TestResolveTaskDateTimeTomorrowAfterMidnightIsNextCalendarDay(t *testing.T) {
	clock := testTaskClock(2026, time.September, 27, 1, 0)
	got, err := resolveTaskDateTime(map[string]any{"date": "tomorrow", "time": "09:00"}, clock, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.At.Day() != 28 || got.At.Month() != time.September {
		t.Fatalf("tomorrow at 1am = %s, want 2026-09-28", got.At)
	}
}

func TestResolveTaskDateTimeRejects(t *testing.T) {
	clock := testTaskClock(2026, time.September, 26, 19, 0)
	for name, input := range map[string]map[string]any{
		"没给时刻":    {"date": "tomorrow"},
		"时刻格式错":   {"date": "tomorrow", "time": "25:00"},
		"日期格式错":   {"date": "下周", "time": "09:00"},
		"本月没有31号": {"date": "31", "time": "09:00"},
		"不存在的日期":  {"date": "2026-02-30", "time": "09:00"},
		"今天已过的时刻": {"date": "today", "time": "08:00"},
		"过去的完整日期": {"date": "2026-09-01", "time": "09:00"},
	} {
		if _, err := resolveTaskDateTime(input, clock, true); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
	// 订阅不要求严格未来：今天 08:00 已过交给订阅自己顺延。
	if _, err := resolveTaskDateTime(map[string]any{"date": "today", "time": "08:00"}, clock, false); err != nil {
		t.Fatalf("non-strict should accept past time: %v", err)
	}
}

func TestApplyTaskDateTimeRewritesItemsAndRejectsConflicts(t *testing.T) {
	clock := testTaskClock(2026, time.September, 26, 19, 0)
	input := map[string]any{
		"operation": "create",
		"items": []any{
			map[string]any{"date": "27", "time": "09:00", "message": "A"},
			map[string]any{"date": "28", "time": "09:00", "message": "B"},
			map[string]any{"delay": "1h", "message": "C"},
		},
	}
	out, notes, err := applyTaskDateTime(input, clock, true)
	if err != nil {
		t.Fatal(err)
	}
	items := out["items"].([]any)
	first := items[0].(map[string]any)
	if first["at"] != "2026-09-27T09:00:00+08:00" || first["date"] != nil || first["time"] != nil {
		t.Fatalf("first = %v", first)
	}
	if items[2].(map[string]any)["delay"] != "1h" || len(notes) != 2 {
		t.Fatalf("items = %v notes = %v", items, notes)
	}
	// 原参数不被改写：工具调用失败重试时模型看到的还是自己传的东西。
	if _, touched := input["items"].([]any)[0].(map[string]any)["at"]; touched {
		t.Fatal("applyTaskDateTime mutated the caller's input")
	}
	if _, _, err := applyTaskDateTime(map[string]any{"date": "tomorrow", "time": "09:00", "delay": "1h"}, clock, true); err == nil {
		t.Fatal("date/time with delay should be rejected")
	}
}

func TestDianaReminderToolCreatesWithDateAndTime(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaReminderTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001", Time: time.Now().Unix()})
	raw, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "date": "tomorrow", "time": "22:00", "message": "睡觉",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaReminderResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	tomorrow := time.Now().AddDate(0, 0, 1)
	item := store.items[0]
	local := item.TriggerAt.In(time.Now().Location())
	if local.Day() != tomorrow.Day() || local.Hour() != 22 || local.Minute() != 0 {
		t.Fatalf("trigger = %s", item.TriggerAt)
	}
	if !strings.Contains(result.Message, "按自然日换算：明天 = "+tomorrow.Format("2006-01-02")) {
		t.Fatalf("message should carry the resolved date: %q", result.Message)
	}
}

func TestDianaScheduleToolCreatesDailyWithTimeOnly(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001", Time: time.Now().Unix()})
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "interval": "1d", "time": "08:00", "query": "提醒用户吃早饭",
	}); err != nil {
		t.Fatal(err)
	}
	item := store.items[0]
	local := item.TriggerAt.In(time.Now().Location())
	if local.Hour() != 8 || local.Minute() != 0 || !item.TriggerAt.After(time.Now()) || time.Until(item.TriggerAt) > 24*time.Hour {
		t.Fatalf("trigger = %s", item.TriggerAt)
	}
}
