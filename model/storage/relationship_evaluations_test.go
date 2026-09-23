// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 好感与画像页按机器人、人、群和结果筛选，按时间倒序往前翻页；过期记录按天清理。
func TestRelationshipEvaluationsFilterPageAndPrune(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Now()
	insert := func(profile, user, group, status string, at time.Time) {
		t.Helper()
		if err := store.RecordRelationshipEvaluation(ctx, assistant.RelationshipEvaluationRecord{
			BotProfileID: profile, UserID: user, GroupID: group, Status: status,
			ProposedDelta: 1, AppliedDelta: 1, BeforeScore: 10, AfterScore: 11, Confidence: 0.9,
			Reason: "测试", Model: "m", MessageText: "你好", CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert("bot-a", "u1", "g1", assistant.RelationshipEvaluationChanged, now.Add(-40*24*time.Hour))
	insert("bot-a", "u1", "g1", assistant.RelationshipEvaluationUnchanged, now.Add(-3*time.Minute))
	insert("bot-a", "u2", "g2", assistant.RelationshipEvaluationChanged, now.Add(-2*time.Minute))
	insert("bot-b", "u1", "g1", assistant.RelationshipEvaluationChanged, now.Add(-time.Minute))

	all, err := store.ListRelationshipEvaluations(ctx, assistant.RelationshipEvaluationFilter{BotProfileID: "bot-a"})
	if err != nil || len(all) != 3 || all[0].UserID != "u2" || all[0].MessageText != "你好" || all[0].Confidence != 0.9 {
		t.Fatalf("scoped list = %#v err=%v", all, err)
	}
	changed, _ := store.ListRelationshipEvaluations(ctx, assistant.RelationshipEvaluationFilter{
		BotProfileID: "bot-a", Statuses: []string{assistant.RelationshipEvaluationChanged, assistant.RelationshipEvaluationCapped},
	})
	if len(changed) != 2 {
		t.Fatalf("changed only = %#v", changed)
	}
	byUser, _ := store.ListRelationshipEvaluations(ctx, assistant.RelationshipEvaluationFilter{UserID: "u1", GroupID: "g1"})
	if len(byUser) != 3 {
		t.Fatalf("by user across bots = %#v", byUser)
	}
	page, _ := store.ListRelationshipEvaluations(ctx, assistant.RelationshipEvaluationFilter{BotProfileID: "bot-a", BeforeID: all[0].ID, Limit: 1})
	if len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("next page = %#v", page)
	}

	deleted, err := store.PruneRelationshipEvaluations(ctx, now.AddDate(0, 0, -30))
	if err != nil || deleted != 1 {
		t.Fatalf("pruned = %d err=%v", deleted, err)
	}
	remaining, _ := store.ListRelationshipEvaluations(ctx, assistant.RelationshipEvaluationFilter{})
	if len(remaining) != 3 {
		t.Fatalf("remaining = %d", len(remaining))
	}
}

// 画像和好感度出自同一次评估：画像生成按 portrait_count 筛，分数没动的也算；
// 按人找支持 QQ 号和昵称的模糊匹配，% 和 _ 按字面匹配。
func TestRelationshipEvaluationsPortraitAndPersonFilters(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	records := []assistant.RelationshipEvaluationRecord{
		{UserID: "10001", SenderName: "小林", GroupID: "g1", Status: assistant.RelationshipEvaluationUnchanged,
			Portrait: []assistant.RelationshipEvaluationPortrait{{Field: "occupation", Label: "职业", Value: "程序员", Source: "stated"}}},
		{UserID: "10002", SenderName: "阿树_100%", GroupID: "g2", Status: assistant.RelationshipEvaluationChanged, AppliedDelta: 1},
		{UserID: "10003", SenderName: "路人", GroupID: "g1", Status: assistant.RelationshipEvaluationUnchanged},
	}
	for _, record := range records {
		if err := store.RecordRelationshipEvaluation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	list := func(filter assistant.RelationshipEvaluationFilter) []assistant.RelationshipEvaluationRecord {
		t.Helper()
		result, err := store.ListRelationshipEvaluations(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	portrait := list(assistant.RelationshipEvaluationFilter{HasPortrait: true})
	if len(portrait) != 1 || len(portrait[0].Portrait) != 1 || portrait[0].Portrait[0].Label != "职业" || portrait[0].Portrait[0].Value != "程序员" {
		t.Fatalf("portrait only = %#v", portrait)
	}
	if byName := list(assistant.RelationshipEvaluationFilter{Query: "小林"}); len(byName) != 1 || byName[0].UserID != "10001" {
		t.Fatalf("by name = %#v", byName)
	}
	if byQQ := list(assistant.RelationshipEvaluationFilter{Query: "1000"}); len(byQQ) != 3 {
		t.Fatalf("by qq prefix = %d", len(byQQ))
	}
	if literal := list(assistant.RelationshipEvaluationFilter{Query: "_100%"}); len(literal) != 1 || literal[0].UserID != "10002" {
		t.Fatalf("literal wildcard = %#v", literal)
	}
	if byGroup := list(assistant.RelationshipEvaluationFilter{GroupID: "g1", HasPortrait: true}); len(byGroup) != 1 {
		t.Fatalf("group + portrait = %#v", byGroup)
	}
}
