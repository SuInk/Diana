// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
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
	q := r.memoryQueryForEvent(context.Background(), event, "部署阻塞", 120)
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
			// search 给全文，不再截成片段；证据留给 read。
			if item.Content != s.neighbors[0].Content || item.Evidence != "" {
				t.Fatalf("search should return full content without evidence: %+v", item)
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

type memorySearchTestPage struct {
	Items []memoryToolItem `json:"items"`
	Total int              `json:"total"`
	toolCursorPage
}

func runMemorySearch(t *testing.T, ctx context.Context, tool *dianaMemoryTool, input map[string]any) memorySearchTestPage {
	t.Helper()
	raw, err := tool.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var got memorySearchTestPage
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMemoryToolSearchPagesWithCursor(t *testing.T) {
	r, _, event := recallFixture()
	tool := &dianaMemoryTool{runtime: r, event: event}
	ctx := context.Background()
	first := runMemorySearch(t, ctx, tool, map[string]any{"operation": "search", "query": "部署阻塞", "limit": 1})
	if len(first.Items) != 1 || first.Total < 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	rest := runMemorySearch(t, ctx, tool, map[string]any{"operation": "search", "query": "部署阻塞", "cursor": first.NextCursor, "limit": 100})
	if len(rest.Items) != first.Total-1 || rest.HasMore || rest.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second page = %+v", rest)
	}
	// 游标绑定检索词：换了 query 还拿旧游标是误用，直接报错。
	if _, err := tool.Run(ctx, map[string]any{"operation": "search", "query": "别的词", "cursor": first.NextCursor}); err == nil {
		t.Fatal("cursor from another query must be rejected")
	}
}

// cappedMemoryStore 照 SQLite 的候选窗口：没设 Unlimited 时只给 MaxCandidates 条。
type cappedMemoryStore struct {
	testStructuredMemoryStore
}

func (s *cappedMemoryStore) ListStructuredMemories(ctx context.Context, query StructuredMemoryQuery) ([]StructuredMemoryItem, error) {
	if len(query.RelatedEntities) > 0 || len(query.RelatedTopics) > 0 || query.SharedPublicOnly {
		return nil, nil
	}
	items, err := s.testStructuredMemoryStore.ListStructuredMemories(ctx, query)
	if err == nil && !query.Unlimited && query.MaxCandidates > 0 && len(items) > query.MaxCandidates {
		items = items[:query.MaxCandidates]
	}
	return items, err
}

func manyDeploymentMemories(count int) []StructuredMemoryItem {
	now := time.Now()
	items := make([]StructuredMemoryItem, 0, count)
	for i := 0; i < count; i++ {
		// 重要度只有四档，大量同分，翻页时靠 id 定先后。
		items = append(items, StructuredMemoryItem{
			ID: fmt.Sprintf("m-%03d", i), Key: fmt.Sprintf("deploy-%d", i), Kind: MemoryKindFact, Topic: "部署阻塞",
			Content: fmt.Sprintf("第 %d 次部署阻塞是因为依赖没装，%s", i, strings.Repeat("细节", 20)), SourceSession: "group:g",
			Visibility: MemoryVisibilitySession, Confidence: 0.95, Importance: 0.5 + float64(i%4)*0.1,
			CreatedAt: now.Add(-time.Duration(i) * time.Hour), LastVerifiedAt: now.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}
	return items
}

// 候选超过自动注入的 120 条窗口、命中超过排序的 24 条名额时，工具照样报真实总数；
// 单次上限很小时按整条切页，翻页之间注入路径回写了 last_verified_at（名次本会变），
// 顺着 cursor 仍不重不漏读完全部。
func TestMemoryToolSearchReadsPastInjectionCaps(t *testing.T) {
	const total = structuredMemoryLoadLimit + 80
	r := NewRuntime(DefaultBotConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &cappedMemoryStore{}
	store.items = manyDeploymentMemories(total)
	r.SetStructuredMemoryStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001"}
	if injected := rankStructuredMemories(store.items[:structuredMemoryLoadLimit], event, "部署阻塞", time.Now()); len(injected) > 24+maximumCoreMemoryItems {
		t.Fatalf("自动注入路径的名额不该变：%d 条", len(injected))
	}
	tool := &dianaMemoryTool{runtime: r, event: event}
	ctx := agent.WithToolOutputBudget(context.Background(), 4000)
	seen := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		input := map[string]any{"operation": "search", "query": "部署阻塞", "limit": 1000}
		if cursor != "" {
			input["cursor"] = cursor
		}
		got := runMemorySearch(t, ctx, tool, input)
		if got.Total != total || len(got.Items) == 0 || len(got.Items) >= total {
			t.Fatalf("page %d = total %d items %d", page, got.Total, len(got.Items))
		}
		for _, item := range got.Items {
			if seen[item.ID] {
				t.Fatalf("page %d repeated %s", page, item.ID)
			}
			seen[item.ID] = true
		}
		if !got.HasMore {
			break
		}
		if !strings.Contains(got.PageNote, "单次结果上限") || got.RemainingCount != total-len(seen) {
			t.Fatalf("page %d note=%q remaining=%d", page, got.PageNote, got.RemainingCount)
		}
		if page == 0 {
			// 模拟自动注入回写命中时间：把排在最后的几条一下子变成「刚命中」。
			store.mu.Lock()
			for i := total - 10; i < total; i++ {
				store.items[i].LastVerifiedAt = time.Now()
			}
			store.mu.Unlock()
		}
		cursor = got.NextCursor
		if page > total {
			t.Fatal("分页没有前进")
		}
	}
	if len(seen) != total {
		t.Fatalf("读完全部页共 %d 条，want %d", len(seen), total)
	}
}
