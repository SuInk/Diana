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
