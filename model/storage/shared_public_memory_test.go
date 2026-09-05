// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestSharedPublicCandidatesCannotBeCrowdedOutByLocalMemory(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	write := func(session, subject, key string, sensitive bool, kind assistant.MemoryKind) {
		t.Helper()
		items, err := s.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
			Session: session, EventKind: assistant.EventKindGroup, GroupID: "group", SubjectUserID: subject, SourceMessageID: key,
			Candidates: []assistant.MemoryCandidate{{Action: assistant.MemoryActionUpsert, Key: key, Kind: kind, Topic: "Aurora", Content: "Aurora release " + key, Confidence: 0.99, Importance: 0.95, Visibility: assistant.MemoryVisibilitySession, Sensitive: sensitive}},
		})
		if err != nil || len(items) != 1 {
			t.Fatalf("write %s: %v", key, err)
		}
	}
	for i := 0; i < 130; i++ {
		write("qq:group:target", "", fmt.Sprintf("local.%d", i), false, assistant.MemoryKindSummary)
	}
	write("qq:group:other", "", "public", false, assistant.MemoryKindSummary)
	write("qq:group:other", "", "secret", true, assistant.MemoryKindSummary)
	write("qq:group:other", "user", "personal", false, assistant.MemoryKindFact)
	write("qq:group:other", "", "rule", false, assistant.MemoryKindInstruction)
	write("qq:group:other", "", "thread", false, assistant.MemoryKindThread)
	write("qq:private:user", "", "private", false, assistant.MemoryKindSummary)
	write("foreign:group:other", "", "foreign", false, assistant.MemoryKindSummary)
	query := assistant.StructuredMemoryQuery{Session: "qq:group:target", SubjectUserID: "user", CrossGroup: true, GroupSessionPrefix: "qq:group:", SharedPublicOnly: true, MaxCandidates: 40, Text: "Aurora", Now: time.Now()}
	items, err := s.ListStructuredMemories(context.Background(), query)
	if err != nil || len(items) != 1 || items[0].Key != "public" {
		t.Fatalf("shared selection: %v err=%v", items, err)
	}
	query.CrossGroup = false
	items, err = s.ListStructuredMemories(context.Background(), query)
	if err != nil || len(items) != 0 {
		t.Fatal("disabled sharing selected public candidates")
	}
	query.CrossPlatformGroupPrefixes = []string{"foreign:group:"}
	items, err = s.ListStructuredMemories(context.Background(), query)
	if err != nil || len(items) != 1 || items[0].Key != "foreign" {
		t.Fatalf("cross-platform pool: %v err=%v", items, err)
	}
	query.CurrentSessionOnly = true
	items, err = s.ListStructuredMemories(context.Background(), query)
	if err != nil || len(items) != 0 {
		t.Fatal("current-only query escaped scope")
	}
}
