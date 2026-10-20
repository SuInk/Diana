// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"math"
	"strings"
	"sync"
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

type moodMemoryStore struct {
	normFallbackStore
	mu     sync.Mutex
	states map[string]MoodSnapshot
	saved  chan struct{}
}

func (s *moodMemoryStore) SaveMoodStates(_ context.Context, states map[string]MoodSnapshot) error {
	s.mu.Lock()
	s.states = states
	s.mu.Unlock()
	s.saved <- struct{}{}
	return nil
}

func (s *moodMemoryStore) LoadMoodStates(context.Context) (map[string]MoodSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states, nil
}

// 被骂掉出开心线后重启，心情要接着上次的分数回落，而不是立刻满血。
func TestMoodSurvivesRestart(t *testing.T) {
	store := &moodMemoryStore{saved: make(chan struct{}, 8)}
	cfg := BotConfig{ID: "bot", MoodEnabled: boolPointer(true)}
	before := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	before.SetMessageHistoryStore(store)
	now := time.Now()
	before.bumpMood("bot", -2, now)
	<-store.saved
	before.bumpMood("bot", -1, now)
	<-store.saved
	want := before.moodScore("bot", now)
	if want >= moodHappyThreshold {
		t.Fatalf("setup should drop below the happy line, got %v", want)
	}

	after := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	after.SetMessageHistoryStore(store)
	if err := after.RestoreMoods(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := after.moodScore("bot", now); math.Abs(got-want) > 1e-9 {
		t.Fatalf("restored mood = %v, want %v", got, want)
	}
	if got := after.moodScore("bot", now.Add(moodHalfLife)); math.Abs(got-(moodBaseline+(want-moodBaseline)/2)) > 1e-9 {
		t.Fatalf("restored mood should keep decaying from its saved time, got %v", got)
	}
}
