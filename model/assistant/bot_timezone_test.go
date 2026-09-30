// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func useSystemLocation(t *testing.T, location *time.Location) {
	t.Helper()
	previous := systemLocation
	systemLocation = func() *time.Location { return location }
	t.Cleanup(func() { systemLocation = previous })
}

// 机器人填的时区优先，其次 TZ，再其次本机时区；本机是裸 UTC（容器默认）时按北京时间。
func TestBotConfigLocationPrecedence(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		name   string
		system *time.Location
		tz     string
		bot    string
		want   string
	}{
		{name: "容器里的裸 UTC 按北京时间", system: time.UTC, want: "Asia/Shanghai"},
		{name: "本机有真实时区就用本机", system: berlin, want: "Europe/Berlin"},
		{name: "TZ 优先于本机", system: berlin, tz: "America/New_York", want: "America/New_York"},
		{name: "显式 TZ=UTC 要尊重", system: time.UTC, tz: "UTC", want: "UTC"},
		{name: "机器人配置最优先", system: berlin, tz: "America/New_York", bot: "Asia/Tokyo", want: "Asia/Tokyo"},
		{name: "填错的时区名当没填", system: time.UTC, tz: "Europe/Berlin", bot: "Mars/Olympus", want: "Europe/Berlin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useSystemLocation(t, tc.system)
			t.Setenv("TZ", tc.tz)
			if got := (BotConfig{Timezone: tc.bot}).Location().String(); got != tc.want {
				t.Fatalf("Location() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestFormatZonedTimeCarriesOffset(t *testing.T) {
	newYork, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 1, 5, 9, 30, 0, 0, newYork)
	if got := formatZonedTime(at, "2006-01-02 15:04"); got != "2026-01-05 09:30（UTC-05:00）" {
		t.Fatalf("formatZonedTime = %q", got)
	}
}

// 容器里是裸 UTC 时，注入的时钟、回复时段和消息时间都按机器人时区，而不是 UTC。
func TestRuntimeUsesBotTimezoneInsteadOfContainerUTC(t *testing.T) {
	useSystemLocation(t, time.UTC)
	t.Setenv("TZ", "")
	now := time.Date(2026, 10, 1, 1, 30, 0, 0, time.UTC) // 东京 10:30，北京 09:30
	runtime := NewRuntime(BotConfig{ID: "tz-bot", Timezone: "Asia/Tokyo"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "tz-bot", UserID: "10001", Time: now.Unix()}

	clock := runtime.runtimeClockPrompt(event)
	for _, want := range []string{"2026-10-01 10:30:00", "UTC+09:00"} {
		if !strings.Contains(clock, want) {
			t.Fatalf("时钟应按机器人时区，缺 %q：%s", want, clock)
		}
	}
	if got := contextMessageTiming(event.Time, 0, profileLocation(event.ProfileID)); !strings.Contains(got, "2026-10-01 10:30:00（UTC+09:00）") {
		t.Fatalf("消息时间应按机器人时区并带偏移：%s", got)
	}
	if got := historyLinePrefix(event); !strings.Contains(got, "2026-10-01 10:30:00") {
		t.Fatalf("历史行应按机器人时区：%s", got)
	}

	// 回复时段 09:00–10:00：东京已经 10:30，不该放行；门禁自己填了北京时间则 09:30 放行。
	gate := ReplyGate{ActiveHoursEnabled: true, ActiveStart: "09:00", ActiveEnd: "10:00"}
	if gate.WithinActiveHours(now.In(runtime.effectiveConfigForEvent(event).Location())) {
		t.Fatal("门禁没填时区时应跟随机器人时区")
	}
	gate.Timezone = "Asia/Shanghai"
	if !gate.WithinActiveHours(now.In(runtime.effectiveConfigForEvent(event).Location())) {
		t.Fatal("门禁自己填的时区应当优先")
	}

	// 「明天 08:00」按机器人时区换算。
	clockForTask := runtime.taskClockForEvent(event)
	if clockForTask.Location.String() != "Asia/Tokyo" {
		t.Fatalf("提醒换算应按机器人时区，得到 %s", clockForTask.Location)
	}
}

// 周期规则的锚点从库里读回来是进程本地时区；按周排时要先换回机器人时区数日子，
// 否则北京时间周一 07:00 在 UTC 还是周日，会排错一天。
func TestRecurringWeekdayRuleUsesBotTimezoneAfterReload(t *testing.T) {
	registerProfileTimezone(BotConfig{ID: "weekly-bot", Timezone: "Asia/Shanghai"})
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	anchor := time.Date(2026, 9, 28, 7, 0, 0, 0, shanghai).UTC() // 周一 07:00，读回来成了 UTC 周日 23:00
	item := Reminder{ProfileID: "weekly-bot", ScheduleAnchorAt: anchor, TriggerAt: anchor, IntervalSeconds: int64(7 * 24 * time.Hour / time.Second), ScheduleWeekdays: []string{"mon"}}
	next := nextRecurringTrigger(item, anchor, anchor.Add(time.Minute)).In(shanghai)
	if next.Weekday() != time.Monday || next.Hour() != 7 {
		t.Fatalf("下一次应是北京时间周一 07:00，得到 %s", next)
	}
}
