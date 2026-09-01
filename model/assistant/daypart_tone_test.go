// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestDayPartBoundaries(t *testing.T) {
	for hour, want := range map[int]dayPart{
		0: dayPartLateNight, 4: dayPartLateNight,
		5: dayPartMorning, 8: dayPartMorning,
		9: dayPartDaytime, 17: dayPartDaytime,
		18: dayPartEvening, 23: dayPartEvening,
	} {
		now := time.Date(2026, 3, 1, hour, 30, 0, 0, time.UTC)
		if got := dayPartAt(now); got != want {
			t.Fatalf("%d 点归到了 %v，应该是 %v", hour, got, want)
		}
	}
}

// TestDaytimeAddsNothing 白天不注入。各风格自己的描述说的就是白天的样子，
// 再补一句「现在是白天，正常说话」只是白占 token。
func TestDaytimeAddsNothing(t *testing.T) {
	noon := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if got := dayPartTonePrompt(true, noon); got != "" {
		t.Fatalf("白天不该注入任何东西，却拿到 %q", got)
	}
	for _, hour := range []int{2, 7, 21} {
		now := time.Date(2026, 3, 1, hour, 0, 0, 0, time.UTC)
		if dayPartTonePrompt(true, now) == "" {
			t.Fatalf("%d 点应该有时段语气", hour)
		}
	}
}

// TestDaypartToneIsOptIn 默认关闭。按时钟改变语气是用户能感知的行为变化，
// 不该在升级后突然发生。
func TestDaypartToneIsOptIn(t *testing.T) {
	midnight := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	if got := dayPartTonePrompt(false, midnight); got != "" {
		t.Fatalf("关掉时还在注入：%q", got)
	}
	if boolValue(DefaultBotConfig().DaypartToneEnabled, true) {
		t.Fatal("默认配置里时段语气不该是打开的")
	}
	if got := dayPartToneForConfig(DefaultBotConfig(), midnight); got != "" {
		t.Fatalf("默认配置下深夜仍注入了 %q", got)
	}
}

// TestDaypartToneUsesReplyGateTimezone 时区复用回复门槛那一份。
//
// 一台机器人不该有两个「几点了」：回复时段按 Asia/Shanghai 判断，语气却按服务器
// UTC 判断的话，会出现「门槛认为是白天所以放行、语气认为是深夜所以装困」。
func TestDaypartToneUsesReplyGateTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区库不可用：%v", err)
	}
	// UTC 20:00 == 上海次日 04:00，一个是傍晚一个是深夜，两边结论必须不同。
	now := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	cfg := BotConfig{
		DaypartToneEnabled: boolPointer(true),
		ReplyGate:          &ReplyGate{Timezone: "Asia/Shanghai"},
	}.WithDefaults()
	got := dayPartToneForConfig(cfg, now)
	if want := dayPartLateNight.prompt(); got != want {
		t.Fatalf("没有按机器人时区判断时段：\n拿到 %q\n应为 %q", got, want)
	}
	if got == dayPartEvening.prompt() {
		t.Fatal("按服务器 UTC 判断了时段")
	}
	_ = shanghai
}

// TestSystemPromptCarriesDaypartTone 确认它真的拼进了提示词。
//
// 常量写得再对，注入点只有一行 appendPromptSection；漏了不会编译错误，表现是
// WebUI 上开关打开却毫无变化。
func TestSystemPromptCarriesDaypartTone(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}
	midnight := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)

	on := NewRuntime(BotConfig{DaypartToneEnabled: boolPointer(true)}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	on.now = func() time.Time { return midnight }
	if prompt := on.systemPrompt(event, nil); !strings.Contains(prompt, "现在是深夜") {
		t.Fatal("打开开关后系统提示词里没有深夜语气")
	}

	off := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	off.now = func() time.Time { return midnight }
	if prompt := off.systemPrompt(event, nil); strings.Contains(prompt, "现在是深夜") {
		t.Fatal("开关关着却注入了深夜语气")
	}
}
