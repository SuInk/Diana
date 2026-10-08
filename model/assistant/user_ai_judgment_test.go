// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"math"
	"testing"
	"time"
)

func TestUserAIJudgmentNeedsSeveralObservations(t *testing.T) {
	now := time.Now()
	var judgment UserAIJudgment
	for i := 0; i < userAIMinObservations-1; i++ {
		judgment = judgment.Observe(0.95, "只在被点名时回复", now)
		if judgment.LikelyAI() {
			t.Fatalf("第 %d 次判断就算作 AI，单次判断会飘，应攒够再用", i+1)
		}
	}
	judgment = judgment.Observe(0.95, "", now)
	if !judgment.LikelyAI() {
		t.Fatalf("攒够 %d 次高分判断后应算作 AI：%+v", userAIMinObservations, judgment)
	}
	if judgment.Reason != "只在被点名时回复" {
		t.Fatalf("空理由不应冲掉上一次的理由：%q", judgment.Reason)
	}
}

func TestUserAIJudgmentSmoothsOutliers(t *testing.T) {
	now := time.Now()
	var judgment UserAIJudgment
	for i := 0; i < 5; i++ {
		judgment = judgment.Observe(0.1, "", now)
	}
	judgment = judgment.Observe(1, "", now)
	if judgment.LikelyAI() || math.Abs(judgment.Likelihood-0.37) > 0.01 {
		t.Fatalf("真人偶尔一批消息像 AI 不该翻案：%+v", judgment)
	}
}

func TestUserAIJudgmentOverrideWins(t *testing.T) {
	now := time.Now()
	judgment := UserAIJudgment{Override: UserAIOverrideHuman}
	for i := 0; i < 5; i++ {
		judgment = judgment.Observe(1, "", now)
	}
	if judgment.LikelyAI() {
		t.Fatal("主人标成真人后，自动判断不应覆盖")
	}
	judgment.Override = UserAIOverrideBot
	judgment.Observations, judgment.Likelihood = 0, 0
	if !judgment.LikelyAI() {
		t.Fatal("主人标成机器人应直接生效")
	}
}

func TestParseMemoryGateResponseReadsSpeakerAI(t *testing.T) {
	candidates, speaker, err := parseMemoryGateResponse(`{"memories":[],"speaker_ai":{"likelihood":0.9,"reason":"群友说它是 bot"}}`)
	if err != nil || len(candidates) != 0 || speaker == nil || speaker.Likelihood != 0.9 || speaker.Reason != "群友说它是 bot" {
		t.Fatalf("parse = %v, %+v, %v", candidates, speaker, err)
	}
	if _, speaker, err := parseMemoryGateResponse(`{"memories":[]}`); err != nil || speaker != nil {
		t.Fatalf("没给 speaker_ai 时应为空：%+v, %v", speaker, err)
	}
}

func TestOtherBotFilterUsesJudgments(t *testing.T) {
	runtime := &Runtime{}
	runtime.likelyAI.once.Do(func() {})
	runtime.rememberUserAI("", "30001", UserAIJudgment{Override: UserAIOverrideBot})
	filter := runtime.otherBotFilter(MessageEvent{}, BotConfig{MarkedBotIDs: []string{"30002"}})
	cases := map[string]bool{"30001": true, "30002": true, "30003": false}
	for userID, want := range cases {
		if got := filter(MessageEvent{UserID: userID}); got != want {
			t.Errorf("filter(%s) = %v, want %v", userID, got, want)
		}
	}
	if !filter(MessageEvent{UserID: "30004", SenderIsBot: true}) {
		t.Error("平台标了机器人的应跳过")
	}
}
