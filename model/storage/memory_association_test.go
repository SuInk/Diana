// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestMemoryAssociationAndIDReadsRespectScopeAndVersions(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	write := func(session, subject, key, entity string, sensitive bool) assistant.StructuredMemoryItem {
		t.Helper()
		items, err := s.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{Session: session, GroupID: "g", SubjectUserID: subject, EventKind: assistant.EventKindGroup, SourceMessageID: key, SourceEventTime: time.Now(), Candidates: []assistant.MemoryCandidate{{Action: assistant.MemoryActionUpsert, Key: key, Kind: assistant.MemoryKindFact, Topic: "交付", Entity: entity, Content: key + " content", Evidence: "meeting excerpt", SourceType: assistant.MemorySourceExplicit, Confidence: 0.99, Importance: 0.9, Visibility: assistant.MemoryVisibilitySession, Sensitive: sensitive}}})
		if err != nil || len(items) != 1 {
			t.Fatalf("write %s: %v", key, err)
		}
		return items[0]
	}
	public := write("bot:group:other", "", "public", "ProjectX", false)
	write("bot:group:other", "", "sensitive", "ProjectX", true)
	write("bot:group:other", "user", "personal", "ProjectX", false)
	foreign := write("foreign:group:other", "", "foreign", "ProjectX", false)
	write("bot:group:other", "", "different", "ProjectY", false)
	q := assistant.StructuredMemoryQuery{Session: "bot:group:current", SubjectUserID: "user", Now: time.Now(), RelatedEntities: []string{"projectx"}, CrossGroup: true, GroupSessionPrefix: "bot:group:"}
	items, err := s.ListStructuredMemories(context.Background(), q)
	if err != nil || len(items) != 1 || items[0].ID != public.ID {
		t.Fatalf("association boundary failed: %v %v", items, err)
	}
	q.RelatedEntities = nil
	q.IDs = []string{foreign.ID}
	items, err = s.ListStructuredMemories(context.Background(), q)
	if err != nil || len(items) != 0 {
		t.Fatal("read by ID bypassed namespace")
	}
	q.IDs = []string{public.ID}
	q.CrossGroup = false
	items, err = s.ListStructuredMemories(context.Background(), q)
	if err != nil || len(items) != 0 {
		t.Fatal("read by ID bypassed disabled cross-group sharing")
	}
	q.CrossGroup = true
	_, err = s.db.Exec(`UPDATE memory_items SET status='superseded' WHERE id=?`, public.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err = s.ListStructuredMemories(context.Background(), q)
	if err != nil || len(items) != 0 {
		t.Fatal("read surfaced superseded memory")
	}
	_, err = s.db.Exec(`UPDATE memory_items SET status='active',expires_at=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), public.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err = s.ListStructuredMemories(context.Background(), q)
	if err != nil || len(items) != 0 {
		t.Fatal("read surfaced expired memory")
	}
}
