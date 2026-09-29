// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestMoodBumpDecayAndThresholds(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "bot", MoodEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)

	// 没人惹它时就在基线上，基线是开心。
	if score := runtime.moodScore("bot", now); score != moodBaseline || score < moodHappyThreshold {
		t.Fatalf("baseline score = %v", score)
	}

	// 轻轻怼一句（-1 按 -1.5 记）还开心。
	runtime.bumpMood("bot", -1, now)
	if score := runtime.moodScore("bot", now); score < moodHappyThreshold {
		t.Fatalf("score after one jab = %v", score)
	}

	// 连着被骂到底也只是平静，不往下欠账。
	for range 10 {
		runtime.bumpMood("bot", -2, now)
	}
	if score := runtime.moodScore("bot", now); score != 0 {
		t.Fatalf("score after insults = %v", score)
	}

	// 没人再惹它，三个小时内自己回到开心。
	if score := runtime.moodScore("bot", now.Add(3*time.Hour)); score < moodHappyThreshold {
		t.Fatalf("score after recovery = %v", score)
	}

	// 半衰期回落：比基线高出的部分两小时后剩一半。
	runtime.bumpMood("bot", 10, now)
	later := now.Add(moodHalfLife)
	want := moodBaseline + (moodScoreLimit-moodBaseline)/2
	if score := runtime.moodScore("bot", later); score < want-0.1 || score > want+0.1 {
		t.Fatalf("decayed score = %v, want %v", score, want)
	}

	// delta 为 0 不刷新时间，也不建条目。
	fresh := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	fresh.bumpMood("bot", 0, now)
	if len(fresh.moods) != 0 {
		t.Fatalf("neutral delta created state: %#v", fresh.moods)
	}

	// 封顶：夸一晚上也顶不破上限。
	for range 100 {
		runtime.bumpMood("bot", 3, now)
	}
	if score := runtime.moodScore("bot", now); score > moodScoreLimit {
		t.Fatalf("score exceeded cap: %v", score)
	}
}

func TestMoodToneForConfigGates(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "bot", MoodEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := runtime.clock()

	// 开着心情时默认就是开心。
	if tone := runtime.moodToneForConfig(runtime.ProfileConfig(""), "bot"); !strings.Contains(tone, "心情不错") {
		t.Fatalf("baseline tone = %q", tone)
	}
	// 被骂狠了掉出开心，只是不注入，没有低落语气。
	for range 10 {
		runtime.bumpMood("bot", -2, now)
	}
	if tone := runtime.moodToneForConfig(runtime.ProfileConfig(""), "bot"); tone != "" {
		t.Fatalf("tone after insults = %q", tone)
	}

	// 总开关关着（默认）时，哪怕心情爆表也不注入。
	off := NewRuntime(BotConfig{ID: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for range 10 {
		off.bumpMood("bot", 3, now)
	}
	if tone := off.moodToneForConfig(off.ProfileConfig(""), "bot"); tone != "" {
		t.Fatalf("disabled tone = %q", tone)
	}
}
