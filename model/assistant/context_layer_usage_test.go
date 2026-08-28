// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func layerMemories(count int, topic string, filler int) []StructuredMemoryItem {
	items := make([]StructuredMemoryItem, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, StructuredMemoryItem{
			ID: topic + string(rune('a'+index)), SubjectUserID: "user",
			Key: "fact.filler", Kind: MemoryKindFact, Topic: topic,
			Content:    strings.Repeat("很长的记忆内容", filler),
			Confidence: 0.98, Importance: 0.9,
		})
	}
	return items
}

// 配额够用时不该报丢弃，且入选 token 要能对上。
func TestRetrievedMemoryLayerReportsFits(t *testing.T) {
	text, usage := formatStructuredMemoryContextWithTokenBudget(
		UserMemoryProfile{UserID: "user", DisplayName: "Alice"},
		RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user"),
		layerMemories(2, "短记忆", 1), 4000)

	if usage.Reason != contextLayerReasonFits {
		t.Fatalf("reason = %q", usage.Reason)
	}
	if usage.SelectedItems != 2 || usage.RankedItems != 2 {
		t.Fatalf("usage = %#v", usage)
	}
	if usage.SelectedTokens == 0 || usage.SelectedTokens != llm.EstimateTextTokens(text) {
		t.Fatalf("selected_tokens = %d, 正文估算 = %d", usage.SelectedTokens, llm.EstimateTextTokens(text))
	}
	if usage.droppedItems() != 0 || len(usage.DroppedSections) != 0 {
		t.Fatalf("配额够用却报了丢弃: %#v", usage)
	}
}

// 配额不够时要说明是本层截断的，而不是让上层的 fits_budget 把它盖过去。
func TestRetrievedMemoryLayerReportsBudgetCut(t *testing.T) {
	items := layerMemories(12, "长记忆", 6)
	_, usage := formatStructuredMemoryContextWithTokenBudget(
		UserMemoryProfile{UserID: "user", DisplayName: "Alice"},
		RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user"),
		items, 400)

	if usage.Reason == contextLayerReasonFits {
		t.Fatalf("本层明显装不下却报了 fits: %#v", usage)
	}
	if usage.SelectedItems >= len(items) {
		t.Fatalf("selected=%d 应当少于候选 %d", usage.SelectedItems, len(items))
	}
	if usage.RankedItems != len(items) || usage.RankedTokens == 0 {
		t.Fatalf("排序阶段的账没记上: %#v", usage)
	}
}

// 分段有固定顺序，前面几条长记忆能让后面整段一条不剩。日志必须能分出
// 「这段本来就没内容」和「这段被挤掉了」。
func TestRetrievedMemoryLayerNamesDroppedSections(t *testing.T) {
	items := append(layerMemories(6, "稳定事实", 8), StructuredMemoryItem{
		ID: "low", SubjectUserID: "user", Key: "fact.low", Kind: MemoryKindFact,
		Topic: "低置信线索", Content: "这条应该落在最后一段",
		Confidence: 0.5, Importance: 0.9, SourceType: MemorySourceInferred,
	})
	_, usage := formatStructuredMemoryContextWithTokenBudget(
		UserMemoryProfile{UserID: "user", DisplayName: "Alice"},
		RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user"),
		items, 300)

	if len(usage.DroppedSections) == 0 {
		t.Fatalf("整段被挤掉却没记下段名: %#v", usage)
	}
	if usage.Reason != contextLayerReasonSectionCut && usage.Reason != contextLayerReasonBudget {
		t.Fatalf("reason = %q", usage.Reason)
	}
}

func TestContextLayerUsageTraceSkipsUnnamedLayers(t *testing.T) {
	trace := contextLayerUsageTrace([]contextLayerUsage{
		{}, // 便签那层没进提示词时是零值，不该在日志里冒出一条空账。
		{Layer: "session_thread", Budget: 1200, CandidateItems: 1, CandidateTokens: 900,
			SelectedItems: 1, SelectedTokens: 900, Reason: contextLayerReasonFits},
	})
	if len(trace) != 1 || trace[0]["layer"] != "session_thread" {
		t.Fatalf("trace = %#v", trace)
	}
	if trace[0]["dropped_tokens"].(int64) != 0 || trace[0]["reason_text"] != "候选全部装入本层配额" {
		t.Fatalf("trace = %#v", trace)
	}
}
