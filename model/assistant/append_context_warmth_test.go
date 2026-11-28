// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 同一模型、间隔相近的续接首步命中低到续接比重组还贵时才重组；样本不够、间隔太短、
// 别的模型的样本都不算。
func TestCacheWarmthRebuildsOnlyWhenMeasuredColdForThatModel(t *testing.T) {
	var warmth cacheWarmth
	for range cacheWarmthMinSamples {
		warmth.observe("cold-model", 40*time.Minute, llm.Usage{InputTokens: 60000, CachedInputTokens: 6000})
		warmth.observe("warm-model", 40*time.Minute, llm.Usage{InputTokens: 60000, CachedInputTokens: 48000})
	}
	if hit, cold := warmth.cold("cold-model", 45*time.Minute, 65000, 37000); !cold || hit > 0.2 {
		t.Fatalf("measured cold cache must rebuild: hit=%v cold=%v", hit, cold)
	}
	if _, cold := warmth.cold("warm-model", 45*time.Minute, 65000, 37000); cold {
		t.Fatal("a model whose cache stays warm must keep continuing")
	}
	if _, cold := warmth.cold("unknown-model", 45*time.Minute, 65000, 37000); cold {
		t.Fatal("without samples keep continuing")
	}
	if _, cold := warmth.cold("cold-model", 2*time.Minute, 65000, 37000); cold {
		t.Fatal("short idle keeps continuing")
	}
	// 相近间隔才算：40 分钟的样本估不了 5 小时。
	if _, cold := warmth.cold("cold-model", 5*time.Hour, 65000, 37000); cold {
		t.Fatal("samples from a different idle range must not be used")
	}
	// 续接并不比重组长多少时，冷一点也照样续接。
	if _, cold := warmth.cold("cold-model", 45*time.Minute, 38000, 37000); cold {
		t.Fatal("continuation barely longer than a rebuild should continue")
	}
}

func TestCacheWarmthKeepsRecentSamples(t *testing.T) {
	var warmth cacheWarmth
	for range cacheWarmthMaxSamples + 10 {
		warmth.observe("m", time.Hour, llm.Usage{InputTokens: 100})
	}
	warmth.observe("m", time.Hour, llm.Usage{})
	warmth.observe("", time.Hour, llm.Usage{InputTokens: 100})
	if got := len(warmth.samples["m"]); got != cacheWarmthMaxSamples {
		t.Fatalf("samples=%d", got)
	}
	if len(warmth.samples[""]) != 0 {
		t.Fatal("samples without a model must be ignored")
	}
}
