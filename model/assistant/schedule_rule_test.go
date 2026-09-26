// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"reflect"
	"testing"
	"time"
)

var ruleTestZone = time.FixedZone("CST", 8*3600)

func ruleTestDate(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, ruleTestZone)
}

func TestScheduleDayRuleDaysInMonth(t *testing.T) {
	cases := []struct {
		name  string
		rule  scheduleDayRule
		year  int
		month time.Month
		want  []int
	}{
		{"最后一天-二月", scheduleDayRule{MonthDays: []int{-1}}, 2026, time.February, []int{28}},
		{"最后一天-闰二月", scheduleDayRule{MonthDays: []int{-1}}, 2028, time.February, []int{29}},
		{"倒数第二天", scheduleDayRule{MonthDays: []int{-2}}, 2026, time.January, []int{30}},
		{"31号在小月取月末", scheduleDayRule{MonthDays: []int{31}}, 2026, time.June, []int{30}},
		{"1号和15号", scheduleDayRule{MonthDays: []int{1, 15}}, 2026, time.June, []int{1, 15}},
		{"1号15号和最后一天", scheduleDayRule{MonthDays: []int{1, 15, -1}}, 2026, time.February, []int{1, 15, 28}},
		{"31号和最后一天在小月合并", scheduleDayRule{MonthDays: []int{31, -1}}, 2026, time.April, []int{30}},
		// 2026-10-01 是周四：第一个周一是 5 号，最后一个周五是 30 号。
		{"第一个周一", scheduleDayRule{Weekday: "mon", Week: 1}, 2026, time.October, []int{5}},
		{"第一个周四就是1号", scheduleDayRule{Weekday: "thu", Week: 1}, 2026, time.October, []int{1}},
		{"第二个周日", scheduleDayRule{Weekday: "sun", Week: 2}, 2026, time.October, []int{11}},
		{"最后一个周五", scheduleDayRule{Weekday: "fri", Week: -1}, 2026, time.October, []int{30}},
		{"最后一个周六就是月末", scheduleDayRule{Weekday: "sat", Week: -1}, 2026, time.October, []int{31}},
		{"倒数第二个周五", scheduleDayRule{Weekday: "fri", Week: -2}, 2026, time.October, []int{23}},
		{"第五个周四存在", scheduleDayRule{Weekday: "thu", Week: 5}, 2026, time.October, []int{29}},
		{"第五个周一不存在", scheduleDayRule{Weekday: "mon", Week: 5}, 2026, time.October, nil},
	}
	for _, tc := range cases {
		got := tc.rule.daysInMonthFor(tc.year, tc.month)
		if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeScheduleDayRule(t *testing.T) {
	rule, err := normalizeScheduleDayRule(scheduleDayRule{Weekdays: []string{"fri", "mon", "wed", "mon"}})
	if err != nil || !reflect.DeepEqual(rule.Weekdays, []string{"mon", "wed", "fri"}) {
		t.Fatalf("weekdays = %v, %v", rule.Weekdays, err)
	}
	rule, err = normalizeScheduleDayRule(scheduleDayRule{MonthDays: []int{-1, 15, 1, -2, 15}})
	if err != nil || !reflect.DeepEqual(rule.MonthDays, []int{1, 15, -2, -1}) {
		t.Fatalf("month days = %v, %v", rule.MonthDays, err)
	}
	for _, bad := range []scheduleDayRule{
		{MonthDays: []int{32}}, {MonthDays: []int{0}}, {MonthDays: []int{1}, Weekday: "mon", Week: 1},
		{Weekdays: []string{"mon"}, MonthDays: []int{1}}, {Weekdays: []string{"xyz"}},
		{Weekday: "mon"}, {Weekday: "mon", Week: 6}, {Weekday: "xyz", Week: 1}, {Week: 1},
	} {
		if _, err := normalizeScheduleDayRule(bad); err == nil {
			t.Fatalf("%+v should be rejected", bad)
		}
	}
}

func TestParseScheduleDayRuleAcceptsToolShapes(t *testing.T) {
	rule, present, err := parseScheduleDayRule(map[string]any{"weekdays": []any{"MON", "fri", "wed"}})
	if err != nil || !present || !reflect.DeepEqual(rule.Weekdays, []string{"mon", "wed", "fri"}) {
		t.Fatalf("weekdays = %+v present=%v err=%v", rule, present, err)
	}
	rule, _, err = parseScheduleDayRule(map[string]any{"month_days": []any{float64(15), float64(1)}})
	if err != nil || !reflect.DeepEqual(rule.MonthDays, []int{1, 15}) {
		t.Fatalf("month_days = %+v err=%v", rule, err)
	}
	rule, _, err = parseScheduleDayRule(map[string]any{"month_days": "1,15"})
	if err != nil || !reflect.DeepEqual(rule.MonthDays, []int{1, 15}) {
		t.Fatalf("month_days string = %+v err=%v", rule, err)
	}
	rule, present, err = parseScheduleDayRule(map[string]any{"weekdays": []any{}, "month_days": []any{}})
	if err != nil || !present || !rule.IsZero() {
		t.Fatalf("empty rule should clear: %+v present=%v err=%v", rule, present, err)
	}
}

// 2026-10-05 是周一。每周一三五 9 点：周三 10 点之后下一次是周五 9 点，再下一次
// 是下周一 9 点。
func TestRuleSlotAfterWeekdays(t *testing.T) {
	rule := scheduleDayRule{Weekdays: []string{"mon", "wed", "fri"}}
	anchor := ruleTestDate(2026, time.October, 5, 9)
	weekly := calendarDuration{Fixed: scheduleWeek}
	got := ruleSlotAfter(anchor, weekly, rule, ruleTestDate(2026, time.October, 7, 10), time.Time{})
	if want := ruleTestDate(2026, time.October, 9, 9); !got.Equal(want) {
		t.Fatalf("after wed = %s, want %s", got, want)
	}
	got = ruleSlotAfter(anchor, weekly, rule, got, time.Time{})
	if want := ruleTestDate(2026, time.October, 12, 9); !got.Equal(want) {
		t.Fatalf("after fri = %s, want %s", got, want)
	}
	// 几个月之后也照样落在一三五。
	got = ruleSlotAfter(anchor, weekly, rule, ruleTestDate(2027, time.March, 3, 9), time.Time{})
	if want := ruleTestDate(2027, time.March, 5, 9); !got.Equal(want) {
		t.Fatalf("far future = %s, want %s", got, want)
	}
}

// 隔周的一三五：第 0 周、第 2 周……跳过中间那周。
func TestRuleSlotAfterBiweeklyWeekdays(t *testing.T) {
	rule := scheduleDayRule{Weekdays: []string{"mon", "fri"}}
	anchor := ruleTestDate(2026, time.October, 7, 9) // 周三：所在那一周从 10-05 起
	biweekly := calendarDuration{Fixed: 2 * scheduleWeek}
	got := ruleSlotAfter(anchor, biweekly, rule, ruleTestDate(2026, time.October, 9, 10), time.Time{})
	if want := ruleTestDate(2026, time.October, 19, 9); !got.Equal(want) {
		t.Fatalf("biweekly = %s, want %s", got, want)
	}
}

func TestRuleSlotAfterMonthDays(t *testing.T) {
	rule := scheduleDayRule{MonthDays: []int{1, 15, -1}}
	anchor := ruleTestDate(2026, time.January, 1, 9)
	monthly := calendarDuration{Months: 1}
	steps := []time.Time{
		ruleTestDate(2026, time.February, 1, 9),
		ruleTestDate(2026, time.February, 15, 9),
		ruleTestDate(2026, time.February, 28, 9),
		ruleTestDate(2026, time.March, 1, 9),
	}
	after := ruleTestDate(2026, time.January, 31, 10)
	for _, want := range steps {
		got := ruleSlotAfter(anchor, monthly, rule, after, time.Time{})
		if !got.Equal(want) {
			t.Fatalf("after %s = %s, want %s", after, got, want)
		}
		after = got
	}
}

func TestRuleSlotAfterSkipsMissingMonths(t *testing.T) {
	anchor := ruleTestDate(2026, time.October, 1, 9)
	fifthMonday := scheduleDayRule{Weekday: "mon", Week: 5}
	// 2026 年 10 月没有第五个周一，下一个是 2026-11-30（周一）。
	got := ruleSlotAfter(anchor, calendarDuration{Months: 1}, fifthMonday, anchor, anchor)
	if want := ruleTestDate(2026, time.November, 30, 9); !got.Equal(want) {
		t.Fatalf("fifth monday = %s, want %s", got, want)
	}
}

// 用户在 10 月 20 日说「每月第一个周一 9 点」：10 月的已经过了，第一次是 11 月 2 日。
func TestFirstScheduleTriggerFirstMondayMonthly(t *testing.T) {
	now := ruleTestDate(2026, time.October, 20, 12)
	first, err := firstScheduleTrigger(ruleTestDate(2026, time.October, 20, 9), calendarDuration{Months: 1}, scheduleDayRule{Weekday: "mon", Week: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := ruleTestDate(2026, time.November, 2, 9); !first.Equal(want) {
		t.Fatalf("first = %s, want %s", first, want)
	}
}

func TestDianaScheduleToolCreatesWeekdaysSubscription(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	at := time.Now().In(ruleTestZone).Add(time.Hour).Truncate(time.Minute)
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "interval": "1w", "at": at.Format(time.RFC3339),
		"weekdays": []any{"mon", "wed", "fri"}, "query": "提醒用户去健身",
	}); err != nil {
		t.Fatal(err)
	}
	item := store.items[0]
	switch item.TriggerAt.Weekday() {
	case time.Monday, time.Wednesday, time.Friday:
	default:
		t.Fatalf("first run on %s", item.TriggerAt.Weekday())
	}
	if item.TriggerAt.Before(at) || item.TriggerAt.Hour() != at.Hour() || item.TriggerAt.Minute() != at.Minute() {
		t.Fatalf("trigger = %s, at = %s", item.TriggerAt, at)
	}
	if label := scheduleRuleLabel(item); label != "每周一、周三、周五" {
		t.Fatalf("label = %q", label)
	}
}

func TestDianaScheduleToolCreatesMonthDaysSubscription(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	at := time.Now().In(ruleTestZone).Add(time.Hour).Truncate(time.Minute)
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "interval": "1mo", "at": at.Format(time.RFC3339),
		"month_days": []any{float64(15), float64(1)}, "query": "提醒用户还信用卡",
	}); err != nil {
		t.Fatal(err)
	}
	item := store.items[0]
	if day := item.TriggerAt.Day(); day != 1 && day != 15 {
		t.Fatalf("first run on day %d", day)
	}
	if label := scheduleRuleLabel(item); label != "每月1号、15号" {
		t.Fatalf("label = %q", label)
	}
}

// 每月最后一天：跑完 1 月 31 日那次，下一次是 2 月 28 日，再下一次 3 月 31 日。
func TestNextRecurringTriggerFollowsLastDayRule(t *testing.T) {
	item := Reminder{
		Kind:              ReminderKindQuery,
		IntervalMonths:    1,
		IntervalSeconds:   int64((30 * 24 * time.Hour) / time.Second),
		ScheduleAnchorAt:  ruleTestDate(2026, time.January, 31, 22),
		ScheduleMonthDays: []int{-1},
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

func TestDianaScheduleToolRejectsMismatchedRules(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	for _, input := range []map[string]any{
		{"operation": "create", "interval": "1w", "at": at, "month_days": []any{float64(-1)}, "query": "x"},
		{"operation": "create", "interval": "1mo", "month_days": []any{float64(-1)}, "query": "x"},
		{"operation": "create", "interval": "1mo", "at": at, "weekdays": []any{"mon"}, "query": "x"},
		{"operation": "create", "interval": "1d", "at": at, "weekdays": []any{"mon"}, "query": "x"},
		{"operation": "create", "interval": "1w", "weekdays": []any{"mon"}, "query": "x"},
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

	if _, err := runtime.updateScheduledQuery("10001", "monthly", map[string]any{"interval": "1w", "month_days": []any{float64(-1)}}); err == nil {
		t.Fatal("month_days with weekly interval should be rejected")
	}
	if store.items[0].IntervalMonths != 1 || len(store.items[0].ScheduleMonthDays) != 0 {
		t.Fatalf("failed update mutated the record: %+v", store.items[0])
	}

	item, err := runtime.updateScheduledQuery("10001", "monthly", map[string]any{"month_days": []any{float64(-1)}})
	if err != nil {
		t.Fatal(err)
	}
	last := daysInMonth(item.TriggerAt.Year(), item.TriggerAt.Month())
	if !reflect.DeepEqual(item.ScheduleMonthDays, []int{-1}) || item.TriggerAt.Day() != last || item.TriggerAt.Hour() != anchor.Hour() {
		t.Fatalf("updated = %+v", item)
	}

	// 换成每周一三五：间隔和规则一起改。
	item, err = runtime.updateScheduledQuery("10001", "monthly", map[string]any{
		"interval": "1w", "weekdays": []any{"mon", "wed", "fri"}, "month_days": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.IntervalMonths != 0 || len(item.ScheduleMonthDays) != 0 || !reflect.DeepEqual(item.ScheduleWeekdays, []string{"mon", "wed", "fri"}) {
		t.Fatalf("switched = %+v", item)
	}
}
