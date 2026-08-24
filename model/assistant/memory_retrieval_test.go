// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSummaryNoLongerCarriesRankingPenalty(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := StructuredMemoryItem{
		Topic: "上下文预算", Content: "讨论了上下文预算的分层取值",
		Confidence: 0.95, Importance: 0.7,
		SourceEventTime: now.Add(-24 * time.Hour), LastVerifiedAt: now.Add(-24 * time.Hour),
	}
	summary := base
	summary.ID, summary.Key, summary.Kind = "s1", "summary.2026-08-21.budget", MemoryKindSummary
	episode := base
	episode.ID, episode.Key, episode.Kind = "e1", "episode.2026-08-21.budget", MemoryKindEpisode

	ranked := rankStructuredMemories([]StructuredMemoryItem{summary, episode}, MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "9",
	}, "上下文预算怎么定的", now)

	scores := map[MemoryKind]float64{}
	for _, item := range ranked {
		scores[item.Kind] = item.RetrievalScore
	}
	if len(scores) != 2 {
		t.Fatalf("expected both kinds to survive ranking: %#v", ranked)
	}
	// 摘要已经有 relatedEpisode 硬门，再扣分是双重压制：命中了也排不上。
	if scores[MemoryKindSummary] < scores[MemoryKindEpisode] {
		t.Fatalf("summary still ranked below an otherwise identical episode: %#v", scores)
	}
}

// 同一次输入必须每次算出同一个分数。这条以前不成立：打分遍历的是 map，浮点加法
// 次序每轮不同，两条同分记忆的胜负落在末位比特上，MMR 的去重扣分随机砸给其中一条。
// 上面那条摘要用例因此大约每二十次失败一次，读起来像排序规则出了问题。
func TestStructuredMemoryRankingIsDeterministic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := StructuredMemoryItem{
		Topic: "上下文预算", Content: "讨论了上下文预算的分层取值",
		Confidence: 0.95, Importance: 0.7,
		SourceEventTime: now.Add(-24 * time.Hour), LastVerifiedAt: now.Add(-24 * time.Hour),
	}
	summary := base
	summary.ID, summary.Key, summary.Kind = "s1", "summary.2026-08-21.budget", MemoryKindSummary
	episode := base
	episode.ID, episode.Key, episode.Kind = "e1", "episode.2026-08-21.budget", MemoryKindEpisode
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9"}

	var first string
	for round := range 200 {
		var shape strings.Builder
		for _, item := range rankStructuredMemories([]StructuredMemoryItem{summary, episode}, event, "上下文预算怎么定的", now) {
			fmt.Fprintf(&shape, "%s=%.12f;", item.Kind, item.RetrievalScore)
		}
		if round == 0 {
			first = shape.String()
			continue
		}
		if shape.String() != first {
			t.Fatalf("round %d ranked differently:\n first = %s\n  got  = %s", round, first, shape.String())
		}
	}
}

func TestMemoryStalenessDecayIsBoundedAndGraced(t *testing.T) {
	if got := memoryStalenessDecay(10 * 24 * time.Hour); got != 0 {
		t.Fatalf("fresh memory decayed by %v", got)
	}
	if got := memoryStalenessDecay(memoryStalenessGraceDays * 24 * time.Hour); got != 0 {
		t.Fatalf("memory at the grace boundary decayed by %v", got)
	}
	mid := memoryStalenessDecay(200 * 24 * time.Hour)
	if mid <= 0 || mid >= memoryStalenessMaxDecay {
		t.Fatalf("mid-life decay = %v, want between 0 and %v", mid, memoryStalenessMaxDecay)
	}
	// 衰减是排后面，不是一笔勾销，所以必须有上限。
	if got := memoryStalenessDecay(20 * 365 * 24 * time.Hour); got != memoryStalenessMaxDecay {
		t.Fatalf("decay is unbounded: %v", got)
	}
}

func TestStaleSummariesRankBelowRecentlyTouchedOnes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := StructuredMemoryItem{
		Kind: MemoryKindSummary, Topic: "上下文预算",
		Content: "讨论了上下文预算的分层取值", Confidence: 0.95, Importance: 0.7,
	}
	fresh := base
	fresh.ID, fresh.Key = "s-fresh", "summary.fresh.budget"
	fresh.SourceEventTime = now.Add(-400 * 24 * time.Hour)
	fresh.LastVerifiedAt = now.Add(-24 * time.Hour)

	stale := base
	stale.ID, stale.Key = "s-stale", "summary.stale.budget"
	stale.SourceEventTime = now.Add(-400 * 24 * time.Hour)
	stale.LastVerifiedAt = now.Add(-400 * 24 * time.Hour)

	ranked := rankStructuredMemories([]StructuredMemoryItem{stale, fresh}, MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "9",
	}, "上下文预算怎么定的", now)

	scores := map[string]float64{}
	for _, item := range ranked {
		scores[item.Key] = item.RetrievalScore
	}
	if scores["summary.fresh.budget"] <= scores["summary.stale.budget"] {
		t.Fatalf("recently touched summary did not outrank the stale one: %#v", scores)
	}
}
