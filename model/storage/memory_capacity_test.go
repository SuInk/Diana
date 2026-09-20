// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func writeMemory(t *testing.T, store *SQLiteStore, key string, importance float64, messageID string) {
	t.Helper()
	_, err := store.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
		SubjectUserID:   "alice",
		SubjectName:     "Alice",
		Session:         "bot:group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: messageID,
		SourceEventTime: time.Now(),
		Candidates: []assistant.MemoryCandidate{{
			Action:     assistant.MemoryActionUpsert,
			Key:        key,
			Kind:       assistant.MemoryKindFact,
			Topic:      "测试",
			Content:    "Alice 的第 " + key + " 条事实",
			Evidence:   key,
			SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.95,
			Importance: importance,
			Visibility: assistant.MemoryVisibilitySession,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func activeMemoryKeys(t *testing.T, store *SQLiteStore) []string {
	t.Helper()
	items, err := store.ListStructuredMemories(context.Background(), assistant.StructuredMemoryQuery{
		SubjectUserID: "alice",
		Session:       "bot:group:123",
		GroupID:       "123",
		Now:           time.Now(),
		MaxCandidates: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	return keys
}

// 记忆只增不减会把检索窗口填满，真正重要的那几条反而挤不进提示词。
// 超过上限时按重要度淘汰最不值钱的那条。
func TestActiveMemoriesAreCappedByImportance(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// 先写满 20 条高重要度的。
	for index := 0; index < maxActiveMemoriesPerSubject; index++ {
		writeMemory(t, store, fmt.Sprintf("fact.high.%02d", index), 0.9, fmt.Sprintf("m%02d", index))
	}
	if keys := activeMemoryKeys(t, store); len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("上限之内不该淘汰任何一条，实际 %d 条", len(keys))
	}

	// 再写一条低重要度的：它自己就是最不值钱的那条，应当立刻被淘汰。
	writeMemory(t, store, "fact.low.99", 0.5, "m99")
	keys := activeMemoryKeys(t, store)
	if len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("活跃条数 = %d，应当守住 %d", len(keys), maxActiveMemoriesPerSubject)
	}
	for _, key := range keys {
		if key == "fact.low.99" {
			t.Fatal("低重要度的新记忆不该挤掉高重要度的老记忆")
		}
	}

	// 写一条高重要度的：它应当进来，挤掉现有里最弱的那条。
	writeMemory(t, store, "fact.top.100", 0.99, "m100")
	keys = activeMemoryKeys(t, store)
	if len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("活跃条数 = %d，应当守住 %d", len(keys), maxActiveMemoriesPerSubject)
	}
	found := false
	for _, key := range keys {
		if key == "fact.top.100" {
			found = true
		}
	}
	if !found {
		t.Fatalf("高重要度的新记忆没进来：%v", keys)
	}
}

// 淘汰只动个人长期记忆，不碰会话摘要和会话便签。
func TestMemoryCapacityLeavesSummariesAlone(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for index := 0; index < 5; index++ {
		if _, err := store.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
			SubjectUserID:   "",
			Session:         "bot:group:123",
			EventKind:       assistant.EventKindGroup,
			GroupID:         "123",
			SourceMessageID: fmt.Sprintf("s%d", index),
			SourceEventTime: time.Now(),
			Candidates: []assistant.MemoryCandidate{{
				Action:     assistant.MemoryActionUpsert,
				Key:        fmt.Sprintf("summary.%d", index),
				Kind:       assistant.MemoryKindSummary,
				Topic:      "会话摘要",
				Content:    "一段摘要",
				SourceType: assistant.MemorySourceSummary,
				Confidence: 0.9,
				Importance: 0.5,
				Visibility: assistant.MemoryVisibilitySession,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < maxActiveMemoriesPerSubject+3; index++ {
		writeMemory(t, store, fmt.Sprintf("fact.n.%02d", index), 0.9, fmt.Sprintf("m%02d", index))
	}
	items, err := store.ListStructuredMemories(context.Background(), assistant.StructuredMemoryQuery{
		Session:       "bot:group:123",
		GroupID:       "123",
		Now:           time.Now(),
		MaxCandidates: 200,
		Kinds:         []assistant.MemoryKind{assistant.MemoryKindSummary},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("摘要被淘汰了：剩 %d 条", len(items))
	}
}
