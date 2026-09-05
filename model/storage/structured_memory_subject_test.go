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

// 控制台人员页按人取长期记忆：不带会话作用域，只要这个人身上还生效的那些。
func TestListStructuredMemoriesBySubject(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	write := func(userID string, session string, candidate assistant.MemoryCandidate, messageID string) {
		t.Helper()
		if _, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			SubjectUserID:   userID,
			SubjectName:     "Alice",
			Session:         session,
			EventKind:       assistant.EventKindGroup,
			GroupID:         "123",
			SourceMessageID: messageID,
			SourceEventTime: time.Now().Add(-time.Hour),
			Candidates:      []assistant.MemoryCandidate{candidate},
		}); err != nil {
			t.Fatal(err)
		}
	}

	write("10001", "group:123", assistant.MemoryCandidate{
		Key: "fact.residence", Kind: assistant.MemoryKindFact, Topic: "居住地",
		Content: "Alice住在杭州", SourceType: assistant.MemorySourceExplicit,
		Confidence: 0.95, Importance: 0.9, Visibility: assistant.MemoryVisibilityUser,
	}, "m1")
	// 另一个会话里的记忆也要出现：人员页问的是「记住了这个人什么」，不是「这一轮
	// 该召回什么」。
	write("10001", "group:456", assistant.MemoryCandidate{
		Key: "preference.food", Kind: assistant.MemoryKindPreference, Topic: "饮食偏好",
		Content: "Alice喜欢吃辣", SourceType: assistant.MemorySourceExplicit,
		Confidence: 0.9, Importance: 0.5, Visibility: assistant.MemoryVisibilitySession,
	}, "m2")
	// thread 是短期会话状态，事件页另有「临时记忆」一栏，人员页不掺进来。
	write("10001", "group:123", assistant.MemoryCandidate{
		Key: assistant.ThreadMemoryKey("group:123"), Kind: assistant.MemoryKindThread, Topic: "会话状态",
		Content: "正在等用户确认", SourceType: assistant.MemorySourceInferred,
		Confidence: 0.6, Importance: 0.4, Visibility: assistant.MemoryVisibilitySession,
	}, "m3")
	write("10002", "group:123", assistant.MemoryCandidate{
		Key: "fact.residence", Kind: assistant.MemoryKindFact, Topic: "居住地",
		Content: "Bob住在北京", SourceType: assistant.MemorySourceExplicit,
		Confidence: 0.95, Importance: 0.9, Visibility: assistant.MemoryVisibilityUser,
	}, "m4")

	items, err := store.ListStructuredMemoriesBySubject(ctx, "10001", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	// 按重要度排，居住地在前。
	if items[0].Content != "Alice住在杭州" || items[1].Content != "Alice喜欢吃辣" {
		t.Fatalf("order = %#v", items)
	}
	for _, item := range items {
		if item.SubjectUserID != "10001" {
			t.Fatalf("leaked another subject: %#v", item)
		}
		if item.Kind == assistant.MemoryKindThread {
			t.Fatalf("thread memory should stay out of the person page: %#v", item)
		}
	}

	if empty, err := store.ListStructuredMemoriesBySubject(ctx, "  ", 50); err != nil || len(empty) != 0 {
		t.Fatalf("blank user = %#v err=%v", empty, err)
	}

	counts, err := store.CountStructuredMemoriesBySubjects(ctx, []string{"10001", "10001", "10002", "  ", "10003"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["10001"] != 2 || counts["10002"] != 1 {
		t.Fatalf("counts = %#v", counts)
	}
	// 一条记忆都没有的人不出现在结果里，调用方按零值读到 0。
	if _, listed := counts["10003"]; listed {
		t.Fatalf("counts should omit users without memories: %#v", counts)
	}
	if counts, err := store.CountStructuredMemoriesBySubjects(ctx, nil); err != nil || len(counts) != 0 {
		t.Fatalf("empty request = %#v err=%v", counts, err)
	}
}
