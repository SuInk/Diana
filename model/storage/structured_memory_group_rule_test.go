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

func groupRuleTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func writeGroupRuleCandidate(t *testing.T, store *SQLiteStore, kind assistant.EventKind, userID string, candidate assistant.MemoryCandidate, at time.Time, messageID string) {
	t.Helper()
	if candidate.Kind == "" {
		candidate.Kind = assistant.MemoryKindInstruction
	}
	if candidate.Action == "" {
		candidate.Action = assistant.MemoryActionUpsert
	}
	candidate.Topic, candidate.SourceType = "说话要求", assistant.MemorySourceExplicit
	candidate.Confidence, candidate.Importance = 0.95, 0.7
	session, groupID := "group:123", "123"
	if kind == assistant.EventKindPrivate {
		session, groupID = "private:"+userID, ""
	}
	if _, err := store.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
		SubjectUserID: userID, SubjectName: "群友" + userID, Session: session, EventKind: kind, GroupID: groupID,
		SourceMessageID: messageID, SourceEventTime: at, Candidates: []assistant.MemoryCandidate{candidate},
	}); err != nil {
		t.Fatal(err)
	}
}

func listGroupRules(t *testing.T, store *SQLiteStore, now time.Time) map[string]assistant.StructuredMemoryItem {
	t.Helper()
	items, err := store.ListStructuredMemories(context.Background(), assistant.StructuredMemoryQuery{
		Session: "group:123", CurrentSessionOnly: true, GroupRulesOnly: true, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]assistant.StructuredMemoryItem{}
	for _, item := range items {
		byKey[item.Key] = item
	}
	return byKey
}

// 本群约定不挂在提要求的人名下，别人也能撤销；个人要求和私聊里的 applies_to 不算。
func TestGroupRuleIsOwnedByGroup(t *testing.T) {
	store := groupRuleTestStore(t)
	now := time.Now().Add(-time.Hour)
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10001", assistant.MemoryCandidate{
		Key: "instruction.group.no.cao", Content: "群友10001要求 Diana 在本群不再说「草」", AppliesTo: assistant.MemoryAudienceGroup,
	}, now, "m1")
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10002", assistant.MemoryCandidate{
		Key: "instruction.reply.style.hamster", Content: "群友10002希望 Diana 跟他说鼠话",
	}, now, "m2")
	// 私聊没有「本群」，applies_to 不生效。
	writeGroupRuleCandidate(t, store, assistant.EventKindPrivate, "10003", assistant.MemoryCandidate{
		Key: "instruction.no.haha", Content: "群友10003不想听 Diana 说哈哈", AppliesTo: assistant.MemoryAudienceGroup,
	}, now, "m3")
	// 不是 instruction 的也不生效。
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10004", assistant.MemoryCandidate{
		Key: "preference.food", Kind: assistant.MemoryKindPreference, Content: "群友10004喜欢吃辣", AppliesTo: assistant.MemoryAudienceGroup,
	}, now, "m4")

	rules := listGroupRules(t, store, now)
	rule, ok := rules["instruction.group.no.cao"]
	if len(rules) != 1 || !ok || rule.SubjectUserID != "" || rule.SubjectName != "" || !assistant.IsGroupRule(rule) {
		t.Fatalf("rules = %#v", rules)
	}

	// 同一个要求别人再提一次，还是那一条，不会挂到第二个人名下。
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10005", assistant.MemoryCandidate{
		Key: "instruction.group.no.cao", Content: "群友10001要求 Diana 在本群不再说「草」", AppliesTo: assistant.MemoryAudienceGroup,
	}, now.Add(time.Minute), "m5")
	if rules := listGroupRules(t, store, now); len(rules) != 1 || rules["instruction.group.no.cao"].SubjectUserID != "" {
		t.Fatalf("rules after repeat = %#v", rules)
	}

	// 另一个群友说又可以说了，撤得掉；模型撤销时没带 applies_to 也一样。
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10002", assistant.MemoryCandidate{
		Action: assistant.MemoryActionForget, Key: "instruction.group.no.cao",
	}, now.Add(2*time.Minute), "m6")
	if rules := listGroupRules(t, store, now); len(rules) != 0 {
		t.Fatalf("group rule survived a forget from another member: %#v", rules)
	}
	// 带着 applies_to 撤销也行。
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10001", assistant.MemoryCandidate{
		Key: "instruction.group.no.cao", Content: "群友10001要求 Diana 在本群不再说「草」", AppliesTo: assistant.MemoryAudienceGroup,
	}, now.Add(3*time.Minute), "m7")
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10002", assistant.MemoryCandidate{
		Action: assistant.MemoryActionForget, Key: "instruction.group.no.cao", AppliesTo: assistant.MemoryAudienceGroup,
	}, now.Add(4*time.Minute), "m8")
	if rules := listGroupRules(t, store, now); len(rules) != 0 {
		t.Fatalf("group rule survived a forget from another member: %#v", rules)
	}
}

// 本群约定有时效：没给期限按 30 天，给得太长也封顶，说了更短的按说的；过期前再提
// 一次往后顺延；过期以后查不到。
func TestGroupRuleExpires(t *testing.T) {
	store := groupRuleTestStore(t)
	day := 24 * time.Hour
	start := time.Now().Add(-10 * day)
	write := func(key string, retention int, at time.Time, messageID string) {
		t.Helper()
		writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10001", assistant.MemoryCandidate{
			Key: key, Content: "群友10001要求 Diana 在本群不再说「" + key + "」", AppliesTo: assistant.MemoryAudienceGroup, RetentionDays: retention,
		}, at, messageID)
	}
	near := func(got, want time.Time) bool { return got.Sub(want).Abs() < time.Minute }

	write("instruction.group.no.cao", 0, start, "m1")
	write("instruction.group.no.haha", 365, start, "m2")
	write("instruction.group.no.beng", 1, start, "m3")
	rules := listGroupRules(t, store, start)
	if !near(rules["instruction.group.no.cao"].ExpiresAt, start.Add(30*day)) {
		t.Fatalf("no retention must default to 30 days: %v", rules["instruction.group.no.cao"].ExpiresAt)
	}
	if !near(rules["instruction.group.no.haha"].ExpiresAt, start.Add(30*day)) {
		t.Fatalf("retention above the cap must be capped: %v", rules["instruction.group.no.haha"].ExpiresAt)
	}
	if !near(rules["instruction.group.no.beng"].ExpiresAt, start.Add(day)) {
		t.Fatalf("a shorter retention must be kept: %v", rules["instruction.group.no.beng"].ExpiresAt)
	}

	again := start.Add(10 * day)
	write("instruction.group.no.cao", 0, again, "m4")
	if got := listGroupRules(t, store, again)["instruction.group.no.cao"].ExpiresAt; !near(got, again.Add(30*day)) {
		t.Fatalf("asking again must extend the deadline: %v", got)
	}
	// 换个说法补一句「今天也别说」会生成新版本，但不能把原来的期限缩短。
	writeGroupRuleCandidate(t, store, assistant.EventKindGroup, "10002", assistant.MemoryCandidate{
		Key: "instruction.group.no.cao", Content: "群友10002要求 Diana 今天在本群也别说「草」", AppliesTo: assistant.MemoryAudienceGroup, RetentionDays: 1,
	}, again.Add(time.Hour), "m5")
	restated := listGroupRules(t, store, again)["instruction.group.no.cao"]
	if restated.Version < 2 || !near(restated.ExpiresAt, again.Add(30*day)) {
		t.Fatalf("a shorter restatement must not cut the deadline: version=%d expires_at=%v", restated.Version, restated.ExpiresAt)
	}
	later := listGroupRules(t, store, start.Add(35*day))
	if _, ok := later["instruction.group.no.haha"]; ok {
		t.Fatalf("expired rule is still returned: %#v", later)
	}
	if _, ok := later["instruction.group.no.cao"]; !ok {
		t.Fatalf("extended rule expired too early: %#v", later)
	}
}
