// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type associativeMemoryStore struct {
	testStructuredMemoryStore
	neighbors []StructuredMemoryItem
	seen      []StructuredMemoryQuery
}

func (s *associativeMemoryStore) ListStructuredMemories(_ context.Context, q StructuredMemoryQuery) ([]StructuredMemoryItem, error) {
	s.seen = append(s.seen, q)
	if len(q.IDs) > 0 {
		for _, batch := range [][]StructuredMemoryItem{s.items, s.neighbors} {
			for _, item := range batch {
				if item.ID == q.IDs[0] {
					return []StructuredMemoryItem{item}, nil
				}
			}
		}
		return nil, nil
	}
	if len(q.RelatedEntities) > 0 || len(q.RelatedTopics) > 0 {
		return s.neighbors, nil
	}
	return s.items, nil
}

func recallFixture() (*Runtime, *associativeMemoryStore, MessageEvent) {
	r := NewRuntime(DefaultBotConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "user"}
	base := StructuredMemoryItem{ID: "seed", Key: "build", Kind: MemoryKindSummary, Entity: "ProjectX", Topic: "部署阻塞", Content: "部署阻塞由项目依赖引起", SourceSession: "group:g", SourceGroupID: "g", Visibility: MemoryVisibilitySession, Confidence: 0.99, Importance: 0.9, LastVerifiedAt: time.Now()}
	neighbor := base
	neighbor.ID, neighbor.Topic, neighbor.Content = "linked", "交付安排", "维护者在星期五确认最终时间"
	neighbor.Evidence = "会议记录：星期五确认"
	private := neighbor
	private.ID = "private"
	private.Sensitive = true
	store := &associativeMemoryStore{neighbors: []StructuredMemoryItem{neighbor, private}}
	store.items = []StructuredMemoryItem{base}
	r.SetStructuredMemoryStore(store)
	return r, store, e
}

func TestMemoryAssociationRecallsDifferentWordsWithoutEmbedding(t *testing.T) {
	r, s, event := recallFixture()
	q := r.memoryQueryForEvent(event, "部署阻塞", 120)
	items := r.expandMemoryAssociations(context.Background(), s, q, event, s.items)
	if len(s.seen) != 1 || len(s.seen[0].RelatedEntities) != 1 || s.seen[0].RelatedEntities[0] != "ProjectX" || s.seen[0].MaxCandidates != 24 {
		t.Fatalf("unbounded or missing association query: %+v", s.seen)
	}
	got := rankStructuredMemories(items, event, q.Text, q.Now)
	found := false
	for _, item := range got {
		if item.ID == "private" {
			t.Fatal("sensitive neighbor included")
		}
		if item.ID == "linked" {
			found = true
			if !strings.Contains(item.RetrievalReason, "关联主题") {
				t.Fatal("missing link reason")
			}
		}
	}
	if !found {
		t.Fatalf("different-word neighbor was lost: %+v", got)
	}
}

func TestMemoryToolSearchThenReadPreservesEvidence(t *testing.T) {
	r, s, event := recallFixture()
	s.neighbors[0].Content = strings.Repeat("完整内容", 100)
	tool := &dianaMemoryTool{runtime: r, event: event}
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "部署阻塞"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []memoryToolItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Items {
		if item.ID == "linked" {
			found = true
			if item.Content != "" || item.Evidence != "" || len([]rune(item.Snippet)) > 160 {
				t.Fatal("search did not return compact index")
			}
		}
	}
	if !found {
		t.Fatalf("linked index missing: %s", raw)
	}
	raw, err = tool.Run(context.Background(), map[string]any{"operation": "read", "id": "linked"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Content != s.neighbors[0].Content || result.Items[0].Evidence != s.neighbors[0].Evidence {
		t.Fatalf("full evidence lost: %s", raw)
	}
	r.cfg.LongTermMemoryEnabled = boolPointer(false)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "read", "id": "linked"}); err == nil {
		t.Fatal("tool bypassed live memory switch")
	}
}

func TestMemoryCompactRecallOnlyWhenRequested(t *testing.T) {
	item := StructuredMemoryItem{ID: "id", Kind: MemoryKindSummary, Content: strings.Repeat("旧事实", 100)}
	if !strings.Contains(formatStructuredMemoryLine(item), item.Content) {
		t.Fatal("non-agent caller lost full memory")
	}
	item.CompactRecall = true
	text := formatStructuredMemoryLine(item)
	if strings.Contains(text, item.Content) || !strings.Contains(text, "memory_id=id") {
		t.Fatal("compact public memory missing read handle")
	}
	if !(RelationshipPolicy{}).allowedAgentToolNames()[dianaMemoryToolName] {
		t.Fatal("read-only memory tool unavailable to ordinary users")
	}
}
