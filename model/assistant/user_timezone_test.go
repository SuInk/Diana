// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestPortraitTimezoneOnlyAcceptsResolvableZones(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		keep  bool
	}{
		{"Europe/Berlin", true},
		{"Asia/Shanghai", true},
		{"UTC", true},
		{"在德国", false},
		{"UTC+2", false},
		{"比机器人晚六小时", false},
	} {
		trait, ok := NormalizePortraitTrait(UserPortraitTrait{Field: PortraitFieldTimezone, Value: tc.value, Source: PortraitSourceStated, Confidence: 0.95}, now)
		if ok != tc.keep {
			t.Fatalf("value %q kept=%v, want %v", tc.value, ok, tc.keep)
		}
		if ok && trait.Label != "时区" {
			t.Fatalf("label = %q", trait.Label)
		}
	}
	if field, _ := NormalizePortraitField("tz"); field != PortraitFieldTimezone {
		t.Fatalf("alias tz = %q", field)
	}
}

func TestPortraitTimezoneLookupAndOffset(t *testing.T) {
	traits := []UserPortraitTrait{
		{Field: PortraitFieldResidence, Value: "柏林"},
		{Field: PortraitFieldTimezone, Value: "Europe/Berlin"},
	}
	location := PortraitTimezone(traits)
	if location == nil || location.String() != "Europe/Berlin" {
		t.Fatalf("location = %v", location)
	}
	if PortraitTimezone([]UserPortraitTrait{{Field: PortraitFieldResidence, Value: "柏林"}}) != nil {
		t.Fatal("residence alone must not produce a timezone")
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	now := time.Date(2026, 9, 16, 1, 30, 0, 0, time.UTC)
	if got := FormatTimezoneOffset(now, location, shanghai); got != "比你晚 6 小时" {
		t.Fatalf("berlin vs shanghai = %q", got)
	}
	if got := FormatTimezoneOffset(now, shanghai, shanghai); got != "和你所在时区相同" {
		t.Fatalf("same zone = %q", got)
	}
	kathmandu, err := time.LoadLocation("Asia/Kathmandu")
	if err == nil {
		if got := FormatTimezoneOffset(now, kathmandu, shanghai); got != "比你晚 2 小时 15 分" {
			t.Fatalf("kathmandu vs shanghai = %q", got)
		}
	}
}

func TestSpeakerTimezonePromptConvertsForTheOtherSide(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, shanghai)
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }

	event := MessageEvent{Kind: EventKindPrivate, UserID: "u1"}
	if prompt := runtime.speakerTimezonePrompt(event, now); prompt != "" {
		t.Fatalf("prompt without a loaded profile = %q", prompt)
	}
	event.userProfileLoaded = true
	event.userProfile = UserMemoryProfile{Portrait: []UserPortraitTrait{{Field: PortraitFieldResidence, Value: "柏林"}}}
	if prompt := runtime.speakerTimezonePrompt(event, now); prompt != "" {
		t.Fatalf("prompt without a recorded timezone = %q", prompt)
	}
	event.userProfile.Portrait = append(event.userProfile.Portrait, UserPortraitTrait{Field: PortraitFieldTimezone, Value: "Europe/Berlin"})
	event.userProfile.Portrait[1].UpdatedAt = now.Add(-3 * 24 * time.Hour)
	prompt := runtime.speakerTimezonePrompt(event, now)
	for _, want := range []string{"Europe/Berlin", "2026-09-16 03:00", "比你晚 6 小时", "按他的当地时间说", "你自己的「现在」仍以上面的运行时钟为准", "记于 2026-09-13（3 天前）", "以他当下说的为准"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "记录较旧") {
		t.Fatalf("fresh record marked stale:\n%s", prompt)
	}
	// 人会搬家：记了很久的时区要提示先确认一次。
	event.userProfile.Portrait[1].UpdatedAt = now.Add(-PortraitTimezoneStaleAfter - 24*time.Hour)
	stale := runtime.speakerTimezonePrompt(event, now)
	if !strings.Contains(stale, "记录较旧") || !strings.Contains(stale, "个月前") {
		t.Fatalf("stale record prompt = %q", stale)
	}
	if clock := runtime.runtimeClockPrompt(event); !strings.Contains(clock, "Europe/Berlin") || !strings.Contains(clock, "2026-09-16 09:00") {
		t.Fatalf("runtime clock prompt = %q", clock)
	}
}

// TestUnknownSpeakerTimezoneBlocksSleepNudge 没记过对方时区时，深夜和清早不许按本机
// 时钟推断对方的作息：群里有人在海外，「该睡了」就是在对着下午三点的人说。
func TestUnknownSpeakerTimezoneBlocksSleepNudge(t *testing.T) {
	cfg := BotConfig{ReplyGate: &ReplyGate{Timezone: "Asia/Shanghai"}}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区库不可用：%v", err)
	}
	lateNight := unknownSpeakerTimezoneNote(cfg, time.Date(2026, 9, 21, 2, 0, 0, 0, shanghai))
	for _, want := range []string{"没有记录当前发言者所在时区", "催他睡"} {
		if !strings.Contains(lateNight, want) {
			t.Fatalf("深夜提示缺少 %q：%s", want, lateNight)
		}
	}
	morning := unknownSpeakerTimezoneNote(cfg, time.Date(2026, 9, 21, 7, 0, 0, 0, shanghai))
	if !strings.Contains(morning, "早上好") {
		t.Fatalf("清早提示缺少时段问候约束：%s", morning)
	}
	// 白天和晚上引不出作息主张，不该白占 token。
	for _, hour := range []int{12, 21} {
		if got := unknownSpeakerTimezoneNote(cfg, time.Date(2026, 9, 21, hour, 0, 0, 0, shanghai)); got != "" {
			t.Fatalf("%d 点注入了多余的时区提示：%s", hour, got)
		}
	}
	// 时区按回复门槛那一份算，不是运行测试的机器所在时区。
	utc := BotConfig{ReplyGate: &ReplyGate{Timezone: "UTC"}}
	if unknownSpeakerTimezoneNote(utc, time.Date(2026, 9, 21, 2, 0, 0, 0, shanghai)) != "" {
		t.Fatal("没有按机器人配置的时区判断时段")
	}
}

// TestRuntimeClockPromptPicksOneTimezoneStance 记过时区走换算，没记过走「别假设」，
// 两条不能同时出现在一轮提示词里。
func TestRuntimeClockPromptPicksOneTimezoneStance(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区库不可用：%v", err)
	}
	now := time.Date(2026, 9, 21, 2, 0, 0, 0, shanghai)
	runtime := NewRuntime(BotConfig{ReplyGate: &ReplyGate{Timezone: "Asia/Shanghai"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }

	event := MessageEvent{Kind: EventKindPrivate, UserID: "u1"}
	if prompt := runtime.runtimeClockPrompt(event); !strings.Contains(prompt, "没有记录当前发言者所在时区") {
		t.Fatalf("画像没加载时也算不知道时区：%s", prompt)
	}
	event.userProfileLoaded = true
	event.userProfile = UserMemoryProfile{Portrait: []UserPortraitTrait{{Field: PortraitFieldTimezone, Value: "Europe/Berlin", UpdatedAt: now.Add(-24 * time.Hour)}}}
	prompt := runtime.runtimeClockPrompt(event)
	if strings.Contains(prompt, "没有记录当前发言者所在时区") {
		t.Fatalf("记过时区却还在说不知道：%s", prompt)
	}
	if !strings.Contains(prompt, "作息相关的话") {
		t.Fatalf("记过时区时没把作息话题钉到对方当地时间：%s", prompt)
	}
}
