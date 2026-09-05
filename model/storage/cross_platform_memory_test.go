// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestCrossPlatformMemorySharesOnlySafePublicGroupItems(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	write := func(session, subject, key string, visibility assistant.MemoryVisibility, sensitive bool, kind assistant.MemoryKind) {
		t.Helper()
		group, eventKind := "source", assistant.EventKindGroup
		if strings.Contains(session, ":private:") {
			group, eventKind = "", assistant.EventKindPrivate
		}
		items, err := s.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
			Session: session, GroupID: group, EventKind: eventKind, SubjectUserID: subject, SourceMessageID: key, SourceEventTime: now,
			Candidates: []assistant.MemoryCandidate{{Action: assistant.MemoryActionUpsert, Key: key, Kind: kind, Content: "项目发布计划 " + key, Topic: "项目发布", SourceType: assistant.MemorySourceExplicit, Confidence: 0.99, Importance: 0.9, Visibility: visibility, Sensitive: sensitive}},
		})
		if err != nil || len(items) != 1 {
			t.Fatalf("write %s: items=%d err=%v", key, len(items), err)
		}
	}
	write("tg:group:source", "", "public", assistant.MemoryVisibilitySession, false, assistant.MemoryKindSummary)
	write("tg:group:source", "", "sensitive", assistant.MemoryVisibilitySession, true, assistant.MemoryKindSummary)
	write("tg:group:source", "", "group-rule", assistant.MemoryVisibilitySession, false, assistant.MemoryKindInstruction)
	write("tg:group:source", "", "thread", assistant.MemoryVisibilitySession, false, assistant.MemoryKindThread)
	write("tg:group:source", "123", "personal", assistant.MemoryVisibilitySession, false, assistant.MemoryKindFact)
	write("tg:group:source", "123", "user-visible", assistant.MemoryVisibilityUser, false, assistant.MemoryKindPreference)
	write("tg:private:123", "", "private", assistant.MemoryVisibilitySession, false, assistant.MemoryKindSummary)
	write("other:group:source", "", "not-opted-in", assistant.MemoryVisibilitySession, false, assistant.MemoryKindSummary)
	write("tg-extra:group:source", "", "prefix-collision", assistant.MemoryVisibilitySession, false, assistant.MemoryKindSummary)
	query := assistant.StructuredMemoryQuery{Session: "qq:group:target", SubjectUserID: "123", Now: now}
	items, err := s.ListStructuredMemories(context.Background(), query)
	if err != nil || len(items) != 0 {
		t.Fatalf("sharing disabled leaked memories: items=%v err=%v", items, err)
	}
	query.CrossPlatformGroupPrefixes = []string{"tg:group:"}
	items, err = s.ListStructuredMemories(context.Background(), query)
	if err != nil || len(items) != 1 || items[0].Key != "public" {
		t.Fatalf("unsafe cross-platform selection: items=%v err=%v", items, err)
	}
}

func TestPersonalMemorySameIDRemainsNamespaced(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, namespace := range []string{"qq", "tg"} {
		written, err := s.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
			Session: namespace + ":group:1", GroupID: "1", EventKind: assistant.EventKindGroup, SubjectUserID: "123", SourceMessageID: namespace,
			Candidates: []assistant.MemoryCandidate{{Action: assistant.MemoryActionUpsert, Key: "preference.food", Topic: "food", SourceType: assistant.MemorySourceExplicit, Kind: assistant.MemoryKindPreference, Content: namespace + " favorite food", Confidence: 0.95, Importance: 0.8, Visibility: assistant.MemoryVisibilityUser}},
		})
		if err != nil || len(written) != 1 {
			t.Fatalf("write: items=%d err=%v", len(written), err)
		}
	}
	for _, namespace := range []string{"qq", "tg"} {
		items, err := s.ListStructuredMemories(context.Background(), assistant.StructuredMemoryQuery{Session: namespace + ":private:123", SubjectUserID: "123", Now: time.Now()})
		if err != nil || len(items) != 1 || items[0].Content != namespace+" favorite food" {
			t.Fatalf("identity collision: namespace=%s items=%v err=%v", namespace, items, err)
		}
	}
}
