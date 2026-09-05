// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestNormalizePortraitTraitRejectsUnusableEntries(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		trait UserPortraitTrait
		ok    bool
	}{
		{name: "empty field", trait: UserPortraitTrait{Value: "住在杭州"}},
		{name: "empty value", trait: UserPortraitTrait{Field: PortraitFieldResidence}},
		{name: "blank value", trait: UserPortraitTrait{Field: PortraitFieldResidence, Value: "   "}},
		{name: "low confidence", trait: UserPortraitTrait{Field: PortraitFieldResidence, Value: "住在杭州", Confidence: 0.4}},
		{name: "stated fact", trait: UserPortraitTrait{Field: PortraitFieldResidence, Value: "住在杭州", Source: PortraitSourceStated, Confidence: 0.95}, ok: true},
		// 模型自造的字段名收进「其他」，而不是整条丢掉。
		{name: "unknown field falls back", trait: UserPortraitTrait{Field: "favorite_color", Value: "喜欢蓝色", Confidence: 0.9}, ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trait, ok := NormalizePortraitTrait(test.trait, now)
			if ok != test.ok {
				t.Fatalf("normalized=%#v ok=%v, want %v", trait, ok, test.ok)
			}
			if !ok {
				return
			}
			if trait.Label == "" || trait.UpdatedAt.IsZero() {
				t.Fatalf("normalized trait is missing label or time: %#v", trait)
			}
		})
	}
	if trait, _ := NormalizePortraitTrait(UserPortraitTrait{Field: "favorite_color", Value: "喜欢蓝色", Confidence: 0.9}, now); trait.Field != PortraitFieldOther {
		t.Fatalf("unknown field = %q, want other", trait.Field)
	}
}

// 容量为 1 的栏必须表现为覆盖：人搬了家，画像里不能新旧两个城市并排留着。
func TestMergePortraitTraitsReplacesSingleValueField(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	traits := MergePortraitTraits(nil, []UserPortraitTrait{
		{Field: PortraitFieldResidence, Value: "住在杭州", Source: PortraitSourceStated, UpdatedAt: base},
	}, base)
	traits = MergePortraitTraits(traits, []UserPortraitTrait{
		{Field: PortraitFieldResidence, Value: "住在上海", Source: PortraitSourceStated, UpdatedAt: base.Add(time.Hour)},
	}, base.Add(time.Hour))
	if len(traits) != 1 || traits[0].Value != "住在上海" {
		t.Fatalf("residence = %#v, want only the newest value", traits)
	}
}

func TestMergePortraitTraitsKeepsNewestWithinCapacity(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	spec, ok := portraitFieldSpec(PortraitFieldHabit)
	if !ok {
		t.Fatal("habit spec is missing")
	}
	var traits []UserPortraitTrait
	for index := 0; index <= spec.Capacity; index++ {
		traits = MergePortraitTraits(traits, []UserPortraitTrait{{
			Field:     PortraitFieldHabit,
			Value:     "习惯" + itoa(index),
			Source:    PortraitSourceStated,
			UpdatedAt: base.Add(time.Duration(index) * time.Hour),
		}}, base)
	}
	if len(traits) != spec.Capacity {
		t.Fatalf("habit count = %d, want %d: %#v", len(traits), spec.Capacity, traits)
	}
	if traits[0].Value != "习惯"+itoa(spec.Capacity) {
		t.Fatalf("newest habit = %q", traits[0].Value)
	}
	for _, trait := range traits {
		if trait.Value == "习惯0" {
			t.Fatalf("oldest habit should have been evicted: %#v", traits)
		}
	}
}

// 同一件事又被说一遍只刷新时间，不占掉栏位里的另一个位置。
func TestMergePortraitTraitsRefreshesDuplicateInPlace(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	traits := MergePortraitTraits(nil, []UserPortraitTrait{
		{Field: PortraitFieldInterest, Value: "喜欢爬山", Source: PortraitSourceInferred, Confidence: 0.86, UpdatedAt: base},
	}, base)
	later := base.Add(48 * time.Hour)
	traits = MergePortraitTraits(traits, []UserPortraitTrait{
		{Field: PortraitFieldInterest, Value: "喜欢爬山", Evidence: "周末又去爬山了", Source: PortraitSourceStated, Confidence: 0.99, UpdatedAt: later},
	}, later)
	if len(traits) != 1 {
		t.Fatalf("duplicate created a second entry: %#v", traits)
	}
	if !traits[0].UpdatedAt.Equal(later) || traits[0].Source != PortraitSourceStated || traits[0].Evidence == "" {
		t.Fatalf("duplicate did not refresh in place: %#v", traits[0])
	}
}

func TestRemovePortraitFieldClearsOnlyThatColumn(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	traits := MergePortraitTraits(nil, []UserPortraitTrait{
		{Field: PortraitFieldResidence, Value: "住在杭州", Source: PortraitSourceStated, UpdatedAt: base},
		{Field: PortraitFieldOccupation, Value: "做后端开发", Source: PortraitSourceStated, UpdatedAt: base},
	}, base)
	remaining, removed := RemovePortraitField(traits, PortraitFieldResidence)
	if removed != 1 || len(remaining) != 1 || remaining[0].Field != PortraitFieldOccupation {
		t.Fatalf("removed=%d remaining=%#v", removed, remaining)
	}
}

func TestFormatPortraitLinesMarksInferredValues(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	traits := MergePortraitTraits(nil, []UserPortraitTrait{
		{Field: PortraitFieldResidence, Value: "住在杭州", Source: PortraitSourceStated, UpdatedAt: base},
		{Field: PortraitFieldOccupation, Value: "做后端开发", Source: PortraitSourceInferred, Confidence: 0.9, UpdatedAt: base},
	}, base)
	lines := FormatPortraitLines(traits)
	if !strings.Contains(lines, "居住地点：住在杭州") {
		t.Fatalf("stated value is not rendered plainly: %q", lines)
	}
	if !strings.Contains(lines, "职业：做后端开发（推断）") {
		t.Fatalf("inferred value is not marked: %q", lines)
	}
	if FormatPortraitLines(nil) != "" {
		t.Fatal("empty portrait should render nothing")
	}
}

// 画像要进提示词，否则记下来也没人用；而且它是固定核心的一部分，只有实在装不下
// 时才让位。原始发言缓冲则一条都不该出现。
func TestUserMemoryContextCarriesPortrait(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	profile := UserMemoryProfile{
		UserID:       "10005",
		DisplayName:  "Alice",
		Favorability: 30,
		MessageCount: 40,
		Memories:     []UserMemoryItem{{Text: "上周问过怎么配置反向代理"}},
		Portrait: MergePortraitTraits(nil, []UserPortraitTrait{
			{Field: PortraitFieldResidence, Value: "住在杭州", Source: PortraitSourceStated, UpdatedAt: base},
		}, base),
	}
	policy := RelationshipPolicyFor(profile, "owner", "10005")
	text := formatUserMemoryContext(profile, policy)
	if !strings.Contains(text, "人员画像") || !strings.Contains(text, "居住地点：住在杭州") {
		t.Fatalf("portrait missing from prompt: %s", text)
	}

	structured := formatStructuredMemoryContext(profile, policy, nil)
	if !strings.Contains(structured, "居住地点：住在杭州") {
		t.Fatalf("portrait missing from structured memory prompt: %s", structured)
	}

	// 原始发言缓冲不进提示词，预算再宽也不该露出来。
	if strings.Contains(text, "上周问过怎么配置反向代理") {
		t.Fatalf("raw message buffer leaked into prompt: %s", text)
	}
	if strings.Contains(structured, "上周问过怎么配置反向代理") {
		t.Fatalf("raw message buffer leaked into structured prompt: %s", structured)
	}

	// 预算够核心加画像时，画像留着。
	budget := llm.EstimateTextTokens(text)
	trimmed := fitUserMemoryCoreToTokenBudget(profile, policy, budget)
	if !strings.Contains(trimmed, "住在杭州") {
		t.Fatalf("portrait should survive a budget that fits it: %s", trimmed)
	}

	// 预算连画像都装不下时，才轮到画像让位，关系核心留到最后。
	tight := llm.EstimateTextTokens(formatUserMemoryContext(UserMemoryProfile{
		UserID: profile.UserID, DisplayName: profile.DisplayName,
		Favorability: profile.Favorability, MessageCount: profile.MessageCount,
	}, policy))
	squeezed := fitUserMemoryCoreToTokenBudget(profile, policy, tight)
	if strings.Contains(squeezed, "住在杭州") {
		t.Fatalf("portrait should be dropped when it does not fit: %s", squeezed)
	}
	if !strings.Contains(squeezed, "好感度：30") {
		t.Fatalf("relationship core should outlive the portrait: %s", squeezed)
	}
}
