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

// 生产上「这鬼天气给我冻成sb了」带进了 19 条记忆，其中 14 条只和它对上了
// 「了」「给」「这」这类单字。单字不能单独构成相关。
func TestSingleCharacterOverlapIsNotRelevance(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{
		{
			ID: "allowance", Kind: MemoryKindEpisode, Topic: "家庭生活费与扣款", SubjectName: "群友甲",
			Content:    "群友甲提到父亲向小姨要了两千元给他发生活费，其中一千元被直接扣除。",
			Confidence: .99, Importance: .62, SourceSession: "group:123", LastVerifiedAt: now,
		},
		{
			ID: "digest", Kind: MemoryKindSummary, Topic: "十月群聊汇总",
			Content:    "这几天群里讨论了远控架构，有人给出了方案，最后做成了演示。",
			Confidence: .99, Importance: .95, SourceSession: "group:123", LastVerifiedAt: now,
		},
		{
			ID: "weather", Kind: MemoryKindEpisode, Topic: "出门淋雨",
			Content:    "群友乙说上周天气突然变冷，出门没带伞淋了雨。",
			Confidence: .99, Importance: .62, SourceSession: "group:123", LastVerifiedAt: now,
		},
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "user"}
	ranked := rankStructuredMemories(items, event, "这鬼天气给我冻成sb了", now)
	if len(ranked) != 1 || ranked[0].ID != "weather" {
		t.Fatalf("single-character overlap was treated as relevance: %#v", ranked)
	}
}

// 查询本身只有单字时没有词可对，单字命中照旧算数。
func TestSingleCharacterQueryStillMatches(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{{
		ID: "cat", Kind: MemoryKindEpisode, Topic: "养宠物",
		Content:    "群友甲家里养了一只猫。",
		Confidence: .99, Importance: .7, SourceSession: "group:123", LastVerifiedAt: now,
	}}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "user"}
	if ranked := rankStructuredMemories(items, event, "猫", now); len(ranked) != 1 {
		t.Fatalf("single-character query lost its only match: %#v", ranked)
	}
}

func TestMemoryRetrievalTextDropsProtocolMarkers(t *testing.T) {
	got := memoryRetrievalText(MessageEvent{}, `<diana-quote id="12345"/>你看 所以说是夸你`)
	if strings.Contains(got, "diana") || strings.Contains(got, "12345") || !strings.Contains(got, "所以说是夸你") {
		t.Fatalf("retrieval text = %q", got)
	}
}
