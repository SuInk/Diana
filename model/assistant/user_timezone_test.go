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

// TestRuntimeClockPromptAddsSpeakerTimezoneOnlyWhenRecorded 记过时区才给换算；没记过时
// 只有机器人自己的时钟，不另加任何作息相关的话——催不催人睡由 SOUL.md 管。
func TestRuntimeClockPromptAddsSpeakerTimezoneOnlyWhenRecorded(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区库不可用：%v", err)
	}
	now := time.Date(2026, 9, 21, 2, 0, 0, 0, shanghai)
	runtime := NewRuntime(BotConfig{ReplyGate: &ReplyGate{Timezone: "Asia/Shanghai"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }

	event := MessageEvent{Kind: EventKindPrivate, UserID: "u1"}
	if prompt := runtime.runtimeClockPrompt(event); strings.Contains(prompt, "当前发言者所在时区") || strings.Contains(prompt, "睡") {
		t.Fatalf("没记过时区时只该有机器人自己的时钟：%s", prompt)
	}
	event.userProfileLoaded = true
	event.userProfile = UserMemoryProfile{Portrait: []UserPortraitTrait{{Field: PortraitFieldTimezone, Value: "Europe/Berlin", UpdatedAt: now.Add(-24 * time.Hour)}}}
	prompt := runtime.runtimeClockPrompt(event)
	if !strings.Contains(prompt, "他那边现在是 2026-09-20 20:00") {
		t.Fatalf("记过时区时没给出对方的当地时间：%s", prompt)
	}
}

// TestRelationshipEvaluatorDerivesTimezoneFromResidence 时区必须能被后台评估自动记进
// 画像：等对方专门报一句「我在 Europe/Berlin」是等不到的，而这一栏一旦空着，
// 模型就只能拿机器人自己的时钟去猜对方几点。
func TestRelationshipEvaluatorDerivesTimezoneFromResidence(t *testing.T) {
	prompt := relationshipEvaluationSystemPrompt
	for _, want := range []string{
		"不用等对方专门报时区",
		"记下了能唯一确定时区的居住地",
		"known_portrait 里还没有 timezone",
		"source=inferred",
		"跨多个时区的国家",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("评估器提示词缺少 %q", want)
		}
	}
	// 字段说明是随 payload 一起发给评估器的，两边不能各说各的。
	var hint string
	for _, spec := range PortraitFieldSpecs() {
		if spec.Field == PortraitFieldTimezone {
			hint = spec.Hint
		}
	}
	if !strings.Contains(hint, "居住城市或国家能唯一确定时区时一并记下") {
		t.Fatalf("时区字段说明没有跟上：%q", hint)
	}
}
