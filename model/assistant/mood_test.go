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

	// 三次 +1 过开心线。
	for range 3 {
		runtime.bumpMood("bot", 1, now)
	}
	if score := runtime.moodScore("bot", now); score < moodHappyThreshold {
		t.Fatalf("score = %v", score)
	}

	// 负面事件权重更大：一次 -2 按 -3 记。
	runtime.bumpMood("bot", -2, now)
	if score := runtime.moodScore("bot", now); score != 0 {
		t.Fatalf("score after insult = %v", score)
	}

	// 半衰期回落：+6 过两个小时剩一半。
	runtime.bumpMood("bot", 3, now)
	runtime.bumpMood("bot", 3, now)
	later := now.Add(moodHalfLife)
	if score := runtime.moodScore("bot", later); score < 2.9 || score > 3.1 {
		t.Fatalf("decayed score = %v", score)
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

	// 平静时一个字都不注入。
	if tone := runtime.moodToneForConfig(runtime.Config(), "bot"); tone != "" {
		t.Fatalf("neutral tone = %q", tone)
	}
	for range 4 {
		runtime.bumpMood("bot", 1, now)
	}
	if tone := runtime.moodToneForConfig(runtime.Config(), "bot"); !strings.Contains(tone, "心情不错") {
		t.Fatalf("happy tone = %q", tone)
	}
	for range 4 {
		runtime.bumpMood("bot", -2, now)
	}
	if tone := runtime.moodToneForConfig(runtime.Config(), "bot"); !strings.Contains(tone, "低落") {
		t.Fatalf("low tone = %q", tone)
	}

	// 总开关关着（默认）时，哪怕心情爆表也不注入。
	off := NewRuntime(BotConfig{ID: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for range 10 {
		off.bumpMood("bot", 3, now)
	}
	if tone := off.moodToneForConfig(off.Config(), "bot"); tone != "" {
		t.Fatalf("disabled tone = %q", tone)
	}
}
