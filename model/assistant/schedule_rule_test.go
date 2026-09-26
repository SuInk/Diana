// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

var ruleTestZone = time.FixedZone("CST", 8*3600)

func ruleTestDate(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, ruleTestZone)
}

func TestScheduleDayRuleDayInMonth(t *testing.T) {
	cases := []struct {
		name  string
		rule  scheduleDayRule
		year  int
		month time.Month
		want  int
		ok    bool
	}{
		{"最后一天-二月", scheduleDayRule{MonthDay: -1}, 2026, time.February, 28, true},
		{"最后一天-闰二月", scheduleDayRule{MonthDay: -1}, 2028, time.February, 29, true},
		{"最后一天-四月", scheduleDayRule{MonthDay: -1}, 2026, time.April, 30, true},
		{"倒数第二天", scheduleDayRule{MonthDay: -2}, 2026, time.January, 30, true},
		{"31号在小月取月末", scheduleDayRule{MonthDay: 31}, 2026, time.June, 30, true},
		{"15号", scheduleDayRule{MonthDay: 15}, 2026, time.June, 15, true},
		// 2026-10-01 是周四：第一个周一是 5 号，最后一个周五是 30 号。
		{"第一个周一", scheduleDayRule{Weekday: "mon", Week: 1}, 2026, time.October, 5, true},
		{"第一个周四就是1号", scheduleDayRule{Weekday: "thu", Week: 1}, 2026, time.October, 1, true},
		{"第二个周日", scheduleDayRule{Weekday: "sun", Week: 2}, 2026, time.October, 11, true},
		{"最后一个周五", scheduleDayRule{Weekday: "fri", Week: -1}, 2026, time.October, 30, true},
		{"最后一个周六就是月末", scheduleDayRule{Weekday: "sat", Week: -1}, 2026, time.October, 31, true},
		{"倒数第二个周五", scheduleDayRule{Weekday: "fri", Week: -2}, 2026, time.October, 23, true},
		{"第五个周四存在", scheduleDayRule{Weekday: "thu", Week: 5}, 2026, time.October, 29, true},
		{"第五个周一不存在", scheduleDayRule{Weekday: "mon", Week: 5}, 2026, time.October, 0, false},
	}
	for _, tc := range cases {
		got, ok := tc.rule.dayInMonth(tc.year, tc.month)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("%s: got %d ok=%v, want %d ok=%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidateScheduleDayRule(t *testing.T) {
	for _, bad := range []scheduleDayRule{
		{MonthDay: 32}, {MonthDay: -32}, {MonthDay: 1, Weekday: "mon", Week: 1},
		{Weekday: "mon"}, {Weekday: "mon", Week: 6}, {Weekday: "xyz", Week: 1}, {Week: 1},
	} {
		if validateScheduleDayRule(bad) == nil {
			t.Fatalf("%+v should be rejected", bad)
		}
	}
}

func TestRuleSlotAfterSkipsMissingMonths(t *testing.T) {
	anchor := ruleTestDate(2026, time.October, 1, 9)
	fifthMonday := scheduleDayRule{Weekday: "mon", Week: 5}
	// 2026 年 10 月没有第五个周一，11 月也没有，下一个是 2026-11-30（周一）。
	got := ruleSlotAfter(anchor, 1, fifthMonday, anchor, anchor)
	if want := ruleTestDate(2026, time.November, 30, 9); !got.Equal(want) {
		t.Fatalf("fifth monday = %s, want %s", got, want)
	}
}

// 用户在 10 月 20 日说「每月第一个周一 9 点」：10 月的已经过了，第一次是 11 月 2 日。
func TestDianaScheduleToolCreatesFirstMondayMonthly(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	now := ruleTestDate(2026, time.October, 20, 12)
	rule := scheduleDayRule{Weekday: "mon", Week: 1}
	first, err := firstScheduleTrigger(ruleTestDate(2026, time.October, 20, 9), calendarDuration{Months: 1}, rule, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := ruleTestDate(2026, time.November, 2, 9); !first.Equal(want) {
		t.Fatalf("first = %s, want %s", first, want)
	}

	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	at := time.Now().In(ruleTestZone).Add(time.Hour).Truncate(time.Minute)
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "interval": "1mo", "at": at.Format(time.RFC3339),
		"weekday": "mon", "week": float64(1), "query": "提醒用户交月报",
	}); err != nil {
		t.Fatal(err)
	}
	item := store.items[0]
	if item.ScheduleWeekday != "mon" || item.ScheduleWeekOrdinal != 1 || item.TriggerAt.Weekday() != time.Monday || item.TriggerAt.Day() > 7 {
		t.Fatalf("item = %+v", item)
	}
	if item.TriggerAt.Before(at) || item.TriggerAt.Hour() != at.Hour() || item.TriggerAt.Minute() != at.Minute() {
		t.Fatalf("trigger = %s, want a first Monday at %s's clock, not before it", item.TriggerAt, at)
	}
	if label := scheduleRuleLabel(item); label != "每月第 1 个周一" {
		t.Fatalf("label = %q", label)
	}
}

// 每月最后一天：跑完 1 月 31 日那次，下一次是 2 月 28 日，再下一次 3 月 31 日。
func TestNextRecurringTriggerFollowsLastDayRule(t *testing.T) {
	item := Reminder{
		Kind:             ReminderKindQuery,
		IntervalMonths:   1,
		IntervalSeconds:  int64((30 * 24 * time.Hour) / time.Second),
		ScheduleAnchorAt: ruleTestDate(2026, time.January, 31, 22),
		ScheduleMonthDay: -1,
	}
	next := nextRecurringTrigger(item, ruleTestDate(2026, time.January, 31, 22), ruleTestDate(2026, time.January, 31, 22).Add(time.Minute))
	if want := ruleTestDate(2026, time.February, 28, 22); !next.Equal(want) {
		t.Fatalf("after jan = %s, want %s", next, want)
	}
	next = nextRecurringTrigger(item, next, next.Add(time.Minute))
	if want := ruleTestDate(2026, time.March, 31, 22); !next.Equal(want) {
		t.Fatalf("after feb = %s, want %s", next, want)
	}
}

func TestDianaScheduleToolRejectsRuleWithoutMonthsOrAt(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	for _, input := range []map[string]any{
		{"operation": "create", "interval": "1w", "at": at, "month_day": float64(-1), "query": "x"},
		{"operation": "create", "interval": "1mo", "month_day": float64(-1), "query": "x"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("input %v should be rejected", input)
		}
	}
	if len(store.items) != 0 {
		t.Fatalf("items = %+v", store.items)
	}
}

func TestUpdateScheduledQueryChangesRuleWithoutTouchingOnFailure(t *testing.T) {
	anchor := time.Now().In(ruleTestZone).Add(24 * time.Hour).Truncate(time.Minute)
	store := &stubReminderStore{items: []Reminder{{
		ID: "monthly", Kind: ReminderKindQuery, OwnerID: "10001", UserID: "10001", Message: "交房租",
		TriggerAt: anchor, IntervalMonths: 1, IntervalSeconds: int64((30 * 24 * time.Hour) / time.Second),
		ScheduleAnchorAt: anchor,
	}}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)

	if _, err := runtime.updateScheduledQuery("10001", "monthly", map[string]any{"interval": "1w", "month_day": float64(-1)}); err == nil {
		t.Fatal("rule with weekly interval should be rejected")
	}
	if store.items[0].IntervalMonths != 1 || store.items[0].ScheduleMonthDay != 0 {
		t.Fatalf("failed update mutated the record: %+v", store.items[0])
	}

	item, err := runtime.updateScheduledQuery("10001", "monthly", map[string]any{"month_day": float64(-1)})
	if err != nil {
		t.Fatal(err)
	}
	last := daysInMonth(item.TriggerAt.Year(), item.TriggerAt.Month())
	if item.ScheduleMonthDay != -1 || item.TriggerAt.Day() != last || item.TriggerAt.Hour() != anchor.Hour() {
		t.Fatalf("updated = %+v", item)
	}
}
