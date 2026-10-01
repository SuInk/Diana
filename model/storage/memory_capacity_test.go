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
	writeMemoryAt(t, store, key, importance, messageID, time.Now())
}

func writeMemoryAt(t *testing.T, store *SQLiteStore, key string, importance float64, messageID string, sourceTime time.Time) {
	t.Helper()
	_, err := store.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
		SubjectUserID:   "alice",
		SubjectName:     "Alice",
		Session:         "bot:group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: messageID,
		SourceEventTime: sourceTime,
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

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

// 记忆只增不减会把检索窗口填满，真正重要的那几条反而挤不进提示词。
// 超过上限时淘汰保留分最低的那条，但刚写入的不淘汰：刚记下就忘等于没记。
func TestActiveMemoriesAreCappedWithoutDroppingFreshWrites(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// 先写满 20 条高重要度的，最后一条重要度稍低。
	for index := 0; index < maxActiveMemoriesPerSubject; index++ {
		importance := 0.9
		if index == 7 {
			importance = 0.6
		}
		writeMemory(t, store, fmt.Sprintf("fact.high.%02d", index), importance, fmt.Sprintf("m%02d", index))
	}
	if keys := activeMemoryKeys(t, store); len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("上限之内不该淘汰任何一条，实际 %d 条", len(keys))
	}

	// 再写一条低重要度的：它留下，挤掉老记忆里最弱的那条。
	writeMemory(t, store, "fact.low.99", 0.5, "m99")
	keys := activeMemoryKeys(t, store)
	if len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("活跃条数 = %d，应当守住 %d", len(keys), maxActiveMemoriesPerSubject)
	}
	if !containsKey(keys, "fact.low.99") {
		t.Fatal("刚写入的记忆当场被淘汰了")
	}
	if containsKey(keys, "fact.high.07") {
		t.Fatalf("应当淘汰老记忆里最弱的 fact.high.07：%v", keys)
	}

	// 下一次写入时，上一轮那条低重要度的不再受保护，按保留分正常竞争。
	writeMemory(t, store, "fact.top.100", 0.99, "m100")
	keys = activeMemoryKeys(t, store)
	if len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("活跃条数 = %d，应当守住 %d", len(keys), maxActiveMemoriesPerSubject)
	}
	if !containsKey(keys, "fact.top.100") || containsKey(keys, "fact.low.99") {
		t.Fatalf("应当留下新写入的 fact.top.100、淘汰 fact.low.99：%v", keys)
	}
}

// 重要度按最近一次被证实的时间衰减：久未提起的老记忆让位给新近的，
// 新近又重要的不会因为别人老而被挤掉。
func TestMemoryCapacityDecaysStaleImportance(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	// 10 条两个月前的高重要度记忆，10 条近期的中等重要度记忆。
	for index := 0; index < maxActiveMemoriesPerSubject/2; index++ {
		writeMemoryAt(t, store, fmt.Sprintf("fact.stale.%02d", index), 0.8, fmt.Sprintf("s%02d", index), now.Add(-60*24*time.Hour))
	}
	for index := 0; index < maxActiveMemoriesPerSubject/2; index++ {
		writeMemoryAt(t, store, fmt.Sprintf("fact.recent.%02d", index), 0.55, fmt.Sprintf("r%02d", index), now.Add(-24*time.Hour))
	}
	// 两次各写一条新的：每次挤掉的都该是两个月没提的老记忆。
	writeMemory(t, store, "fact.new.a", 0.5, "na")
	writeMemory(t, store, "fact.new.b", 0.5, "nb")
	keys := activeMemoryKeys(t, store)
	if len(keys) != maxActiveMemoriesPerSubject {
		t.Fatalf("活跃条数 = %d，应当守住 %d", len(keys), maxActiveMemoriesPerSubject)
	}
	stale := 0
	for _, key := range keys {
		if len(key) > len("fact.stale.") && key[:len("fact.stale.")] == "fact.stale." {
			stale++
		}
	}
	if stale != maxActiveMemoriesPerSubject/2-2 {
		t.Fatalf("应当淘汰 2 条久未提起的老记忆，剩 %d 条：%v", stale, keys)
	}
	for index := 0; index < maxActiveMemoriesPerSubject/2; index++ {
		if !containsKey(keys, fmt.Sprintf("fact.recent.%02d", index)) {
			t.Fatalf("近期记忆被挤掉了：%v", keys)
		}
	}
	if !containsKey(keys, "fact.new.a") || !containsKey(keys, "fact.new.b") {
		t.Fatalf("新记忆没留下：%v", keys)
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
