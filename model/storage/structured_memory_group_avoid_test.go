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

// 按 key 前缀取群里「别再说」的要求：谁提的都要取到，同样带 avoid 的个人要求不能混进来。
func TestListStructuredMemoriesByKeyPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	write := func(userID, key, content, messageID string) {
		t.Helper()
		if _, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			SubjectUserID: userID, SubjectName: "群友" + userID, Session: "group:123",
			EventKind: assistant.EventKindGroup, GroupID: "123", SourceMessageID: messageID,
			SourceEventTime: time.Now().Add(-time.Hour),
			Candidates: []assistant.MemoryCandidate{{
				Key: key, Kind: assistant.MemoryKindInstruction, Topic: "说话要求", Content: content,
				SourceType: assistant.MemorySourceExplicit, Confidence: 0.95, Importance: 0.7,
				Visibility: assistant.MemoryVisibilitySession,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 模型写成下划线也要落到同一个前缀下。
	write("10001", "instruction.group_avoid.草", "群友10001要求 Diana 在本群不再说「草」", "m1")
	write("10002", "instruction.avoid.sleep.reminders", "群友10002不喜欢 Diana 劝他睡觉", "m2")
	write("10002", "instruction.reply.style.hamster", "群友10002希望 Diana 跟他说鼠话", "m3")

	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		Session: "group:123", SubjectUserID: "10002", CurrentSessionOnly: true, Now: time.Now(),
		Kinds: []assistant.MemoryKind{assistant.MemoryKindInstruction}, KeyPrefix: "instruction.group.avoid.",
		MaxCandidates: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "instruction.group.avoid.草" || items[0].SubjectUserID != "10001" {
		t.Fatalf("items = %#v", items)
	}
}

// 群里「别再说」的要求有时效：没给期限按 30 天，给得太长也封顶，说了更短的就按说的；
// 过期前再提一次往后顺延；过期以后回复里就查不到了。
func TestGroupAvoidMemoryExpires(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	day := 24 * time.Hour
	start := time.Now().Add(-10 * day)
	write := func(key, content string, retention int, at time.Time, messageID string) {
		t.Helper()
		if _, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			SubjectUserID: "10001", SubjectName: "群友10001", Session: "group:123",
			EventKind: assistant.EventKindGroup, GroupID: "123", SourceMessageID: messageID, SourceEventTime: at,
			Candidates: []assistant.MemoryCandidate{{
				Key: key, Kind: assistant.MemoryKindInstruction, Topic: "说话要求", Content: content,
				SourceType: assistant.MemorySourceExplicit, Confidence: 0.95, Importance: 0.7,
				Visibility: assistant.MemoryVisibilitySession, RetentionDays: retention,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(now time.Time) map[string]assistant.StructuredMemoryItem {
		t.Helper()
		items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
			Session: "group:123", CurrentSessionOnly: true, Now: now,
			Kinds: []assistant.MemoryKind{assistant.MemoryKindInstruction}, KeyPrefix: assistant.GroupAvoidMemoryKeyPrefix,
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
	near := func(got, want time.Time) bool { return got.Sub(want).Abs() < time.Minute }

	write("instruction.group.avoid.草", "群友10001要求 Diana 在本群不再说「草」", 0, start, "m1")
	write("instruction.group.avoid.哈哈哈", "群友10001要求 Diana 在本群少发「哈哈哈」", 365, start, "m2")
	write("instruction.group.avoid.绷", "群友10001要求 Diana 今天在本群别说「绷不住了」", 1, start, "m3")

	items := list(start)
	if len(items) != 3 {
		t.Fatalf("items = %#v", items)
	}
	if got := items["instruction.group.avoid.草"].ExpiresAt; !near(got, start.Add(30*day)) {
		t.Fatalf("no retention given must default to 30 days, expires_at = %v", got)
	}
	if got := items["instruction.group.avoid.哈哈哈"].ExpiresAt; !near(got, start.Add(30*day)) {
		t.Fatalf("retention above the cap must be capped, expires_at = %v", got)
	}
	if got := items["instruction.group.avoid.绷"].ExpiresAt; !near(got, start.Add(day)) {
		t.Fatalf("a shorter retention must be kept, expires_at = %v", got)
	}

	// 十天后同一句再提一次，期限从这次往后算。
	again := start.Add(10 * day)
	write("instruction.group.avoid.草", "群友10001要求 Diana 在本群不再说「草」", 0, again, "m4")
	if got := list(again)["instruction.group.avoid.草"].ExpiresAt; !near(got, again.Add(30*day)) {
		t.Fatalf("asking again must extend the deadline, expires_at = %v", got)
	}

	later := start.Add(35 * day)
	items = list(later)
	if _, ok := items["instruction.group.avoid.哈哈哈"]; ok {
		t.Fatalf("expired request is still returned: %#v", items)
	}
	if _, ok := items["instruction.group.avoid.草"]; !ok {
		t.Fatalf("extended request expired too early: %#v", items)
	}
}
