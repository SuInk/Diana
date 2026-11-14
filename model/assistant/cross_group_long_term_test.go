// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestCrossGroupContextKeepsEveryMatchedMessage(t *testing.T) {
	now := time.Now().Unix()
	old := crossGroupTestEvent(now-180*86400, "other", "author", "old", "Aurora release Friday")
	store := &crossGroupHistoryStore{candidates: []MessageEvent{old}}
	for i := int64(1); i <= 8; i++ {
		store.candidates = append(store.candidates, crossGroupTestEvent(now-i, "other", "author", string(rune('a'+i)), "Aurora unrelated discussion"))
	}
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, &crossGroupMembershipChannel{allowed: map[string]bool{"current|author": true}}, NewPluginManager(), nil, nil, nil, nil)
	current := crossGroupTestEvent(now, "current", "requester", "query", "Aurora release Friday")
	got := r.crossGroupContextEvents(current, store)
	if store.query.FromTime != 0 || store.query.ExcludeSession != sessionKey(current) {
		t.Fatalf("query still limited: %+v", store.query)
	}
	if len(got) != len(store.candidates) {
		t.Fatalf("通过判定的跨群消息应全部带上：selected=%d candidates=%d", len(got), len(store.candidates))
	}
	found := false
	for _, e := range got {
		if strings.Contains(e.RawMessage, "Friday") {
			found = true
		}
	}
	if !found {
		t.Fatal("old high-relevance message was dropped")
	}
}

func TestCrossGroupSemanticRecallAndFallback(t *testing.T) {
	store := &semanticFakeStore{semantic: []MessageEvent{crossGroupTestEvent(10, "other", "author", "semantic", "正式开放安排在周五")}}
	embeds := 0
	r := newSemanticRuntime(t, true, store, &embeds)
	mutateTestProfile(r, func(c *BotConfig) { c.CrossGroupMemoryEnabled = boolPointer(true) })
	r.channel = &crossGroupMembershipChannel{allowed: map[string]bool{"current|author": true}}
	mutateTestProfile(r, func(c *BotConfig) { c.DebugModeEnabled = boolPointer(true) })
	logs := &captureAppLogs{}
	r.SetAppLogWriter(logs)
	current := crossGroupTestEvent(180*86400, "current", "requester", "query", "什么时候上线")
	got := r.crossGroupContextEvents(current, store)
	if len(got) != 1 || !strings.Contains(got[0].RawMessage, "周五") || embeds != 1 {
		t.Fatalf("semantic-only hit lost: %#v embeds=%d", got, embeds)
	}
	if store.vectorQuery.MinSimilarity != crossGroupSemanticMinSimilarity || store.vectorQuery.FromTime != 0 || store.vectorQuery.ExcludeSession != sessionKey(current) {
		t.Fatalf("unsafe vector query: %+v", store.vectorQuery)
	}
	meta := crossGroupTraceEntry(t, logs)
	if meta["semantic_candidates"] != 1 || meta["selected"] != 1 || meta["semantic_status"] != "已执行" {
		t.Fatalf("missing semantic diagnostics: %v", meta)
	}
	if len(meta["selected_messages"].([]map[string]any)) != 1 {
		t.Fatal("missing selected message identity")
	}
	store.keyword = []MessageEvent{crossGroupTestEvent(20, "other", "author", "keyword", "什么时候上线，计划周五")}
	r.embedTexts = func(context.Context, llm.ProviderConfig, []string) ([][]float32, error) {
		return nil, errors.New("offline")
	}
	// 换一条消息：同一条消息的结果会被短时复用，见 crossGroupResultCache。
	current.MessageID = "query-2"
	got = r.crossGroupContextEvents(current, store)
	if len(got) != 1 || !strings.Contains(got[0].RawMessage, "计划周五") {
		t.Fatalf("embedding failure lost keyword recall: %v", got)
	}
}

func TestCrossGroupSemanticHitsStillRequireMembership(t *testing.T) {
	store := &semanticFakeStore{semantic: []MessageEvent{crossGroupTestEvent(10, "other", "stranger", "semantic", "正式开放安排在周五")}}
	r := newSemanticRuntime(t, true, store, nil)
	mutateTestProfile(r, func(c *BotConfig) { c.CrossGroupMemoryEnabled = boolPointer(true) })
	r.channel = &crossGroupMembershipChannel{}
	if got := r.crossGroupContextEvents(crossGroupTestEvent(100, "current", "requester", "query", "什么时候上线"), store); len(got) != 0 {
		t.Fatalf("semantic recall bypassed membership: %v", got)
	}
}

// 跨群历史常驻带一部分，更多的去 chat_history 查：这句写在 chat_history 的描述里
// （目录写全文），不再另写进系统提示词。
func TestCrossGroupMoreHintLivesInChatHistory(t *testing.T) {
	if desc := (&dianaChatHistoryTool{}).Description(); !strings.Contains(desc, "scope=all_groups") {
		t.Fatalf("chat_history description = %q", desc)
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "20001"}
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if head, _ := r.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, nil); strings.Contains(head, "标着「跨群历史」的行") {
		t.Fatal("系统提示词里不该再另写跨群历史查更多")
	}
}

// pagedSearchStore 模拟存储层的分页：每页最多 pageSize 条，要按 Offset 翻。
type pagedSearchStore struct {
	crossGroupHistoryStore
	pageSize int
	offsets  []int
}

func (s *pagedSearchStore) SearchMessageEvents(_ context.Context, query MessageHistorySearchQuery) ([]MessageEvent, int, error) {
	s.offsets = append(s.offsets, query.Offset)
	end := min(query.Offset+s.pageSize, len(s.candidates))
	if query.Offset >= end {
		return nil, len(s.candidates), nil
	}
	return append([]MessageEvent(nil), s.candidates[query.Offset:end]...), len(s.candidates), nil
}

func TestSearchAllMessageEventsReadsEveryPage(t *testing.T) {
	store := &pagedSearchStore{pageSize: 3}
	for i := int64(1); i <= 8; i++ {
		store.candidates = append(store.candidates, crossGroupTestEvent(i, "other", "author", string(rune('a'+i)), "Aurora"))
	}
	got, err := searchAllMessageEvents(context.Background(), store, MessageHistorySearchQuery{Text: "Aurora"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 || got[7].MessageID != store.candidates[7].MessageID {
		t.Fatalf("分页没取全：got=%d offsets=%v", len(got), store.offsets)
	}
	if want := []int{0, 3, 6}; len(store.offsets) != len(want) || store.offsets[1] != 3 || store.offsets[2] != 6 {
		t.Fatalf("offsets=%v want %v", store.offsets, want)
	}
}

// timelineHistoryStore 在跨群测试存储上补一条原群时间线，供取前后文。
type timelineHistoryStore struct {
	crossGroupHistoryStore
	timeline []MessageEvent
}

func (s *timelineHistoryStore) ListMessageEventsBetween(context.Context, string, int64, int64) ([]MessageEvent, error) {
	return append([]MessageEvent(nil), s.timeline...), nil
}

func TestCrossGroupNeighborsKeepFullText(t *testing.T) {
	long := strings.Repeat("前文很长的一句话，", 20)
	hit := crossGroupTestEvent(100, "other", "author", "hit", "Aurora release Friday")
	store := &timelineHistoryStore{timeline: []MessageEvent{
		crossGroupTestEvent(90, "other", "author", "before", long),
		hit,
	}}
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, &crossGroupMembershipChannel{allowed: map[string]bool{"current|author": true}}, NewPluginManager(), nil, nil, nil, nil)
	current := crossGroupTestEvent(200, "current", "requester", "query", "Aurora release Friday")
	neighbors := r.crossGroupNeighbors(current, store, []MessageEvent{hit})
	if len(neighbors) != 1 || len(neighbors[0][0]) != 1 {
		t.Fatalf("前文没取到：%#v", neighbors)
	}
	if got := neighbors[0][0][0].text; got != strings.TrimSpace(long) {
		t.Fatalf("前文被截短：%q", got)
	}
}
