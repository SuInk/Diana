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

func TestCrossGroupLongTermRelevanceBeatsRecentWeakMatches(t *testing.T) {
	now := time.Now().Unix()
	old := crossGroupTestEvent(now-180*86400, "other", "author", "old", "Aurora release Friday")
	store := &crossGroupHistoryStore{candidates: []MessageEvent{old}}
	for i := int64(1); i <= 6; i++ {
		store.candidates = append(store.candidates, crossGroupTestEvent(now-i, "other", "author", string(rune('a'+i)), "Aurora unrelated discussion"))
	}
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, &crossGroupMembershipChannel{allowed: map[string]bool{"current|author": true}}, NewPluginManager(), nil, nil, nil, nil)
	current := crossGroupTestEvent(now, "current", "requester", "query", "Aurora release Friday")
	got := r.crossGroupContextEvents(current, store)
	if store.query.FromTime != 0 || store.query.ExcludeSession != sessionKey(current) {
		t.Fatalf("query still limited: %+v", store.query)
	}
	if len(got) != 4 {
		t.Fatalf("selected=%d", len(got))
	}
	found := false
	for _, e := range got {
		if strings.Contains(e.RawMessage, "Friday") {
			found = true
		}
	}
	if !found {
		t.Fatal("old high-relevance message was displaced by recent weak matches")
	}
}

func TestCrossGroupSemanticRecallAndFallback(t *testing.T) {
	store := &semanticFakeStore{semantic: []MessageEvent{crossGroupTestEvent(10, "other", "author", "semantic", "正式开放安排在周五")}}
	embeds := 0
	r := newSemanticRuntime(t, true, store, &embeds)
	r.cfg.CrossGroupMemoryEnabled = boolPointer(true)
	r.channel = &crossGroupMembershipChannel{allowed: map[string]bool{"current|author": true}}
	r.cfg.DebugModeEnabled = true
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
	got = r.crossGroupContextEvents(current, store)
	if len(got) != 1 || !strings.Contains(got[0].RawMessage, "计划周五") {
		t.Fatalf("embedding failure lost keyword recall: %v", got)
	}
}

func TestCrossGroupSemanticHitsStillRequireMembership(t *testing.T) {
	store := &semanticFakeStore{semantic: []MessageEvent{crossGroupTestEvent(10, "other", "stranger", "semantic", "正式开放安排在周五")}}
	r := newSemanticRuntime(t, true, store, nil)
	r.cfg.CrossGroupMemoryEnabled = boolPointer(true)
	r.channel = &crossGroupMembershipChannel{}
	if got := r.crossGroupContextEvents(crossGroupTestEvent(100, "current", "requester", "query", "什么时候上线"), store); len(got) != 0 {
		t.Fatalf("semantic recall bypassed membership: %v", got)
	}
}
