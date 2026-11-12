// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 缓存留多久按模型从实际命中里学：没见过未命中之前不判过期；见过以后，间隔达到它、
// 又超过「还命中过」的最长间隔才判；比 5 分钟短的未命中不学；更长的间隔又命中了就
// 推翻之前的结论；每隔几次照常续接一次用来纠错。
func TestAppendContextCacheLifeLearnsPerModel(t *testing.T) {
	var life appendContextCacheLife
	hit := llm.Usage{InputTokens: 1000, CachedInputTokens: 900}
	miss := llm.Usage{InputTokens: 1000}

	if life.expired("swe", time.Hour) {
		t.Fatal("nothing learned yet: must not expire")
	}
	life.observe("swe", 2*time.Minute, hit, 1)
	life.observe("swe", time.Minute, miss, 1) // 5 分钟内的未命中是偶发，不学
	if life.expired("swe", time.Hour) {
		t.Fatal("a miss below the floor must not teach expiry")
	}
	life.observe("swe", 6*time.Minute, miss, 3) // 多步的轮次不拿来判未命中
	if life.expired("swe", time.Hour) {
		t.Fatal("a multi-step turn must not teach expiry")
	}
	life.observe("swe", 6*time.Minute, miss, 1)
	for name, gap := range map[string]time.Duration{"below floor": 4 * time.Minute, "below the observed miss": 5*time.Minute + 30*time.Second} {
		if life.expired("swe", gap) {
			t.Fatalf("%s must not expire", name)
		}
	}
	if !life.expired("swe", 7*time.Minute) {
		t.Fatal("a gap past the observed miss must expire")
	}
	// 别的模型各学各的。
	if life.expired("terra", 7*time.Minute) {
		t.Fatal("another model has learned nothing")
	}
	// 每 appendContextCacheProbeEvery 次放行一次。
	probes := 0
	for range 2 * appendContextCacheProbeEvery {
		if !life.expired("swe", 7*time.Minute) {
			probes++
		}
	}
	if probes != 2 {
		t.Fatalf("probes = %d, want 2", probes)
	}
	// 探测那一轮命中了：之前的未命中是偶发，推翻。
	life.observe("swe", 7*time.Minute, hit, 1)
	if life.expired("swe", 7*time.Minute) || life.expired("swe", time.Hour) {
		t.Fatal("a hit at a longer gap must clear the learned expiry")
	}
	life.observe("swe", 20*time.Minute, miss, 1)
	if life.expired("swe", 10*time.Minute) || !life.expired("swe", 21*time.Minute) {
		t.Fatal("expiry must follow the new, longer observation")
	}
}
