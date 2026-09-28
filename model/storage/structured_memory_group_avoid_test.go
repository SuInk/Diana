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
