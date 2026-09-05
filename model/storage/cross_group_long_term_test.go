// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestHistorySearchExcludesCurrentSessionBeforeLimit(t *testing.T) {
	for _, fts := range []bool{false, true} {
		t.Run(fmt.Sprint(fts), func(t *testing.T) {
			s := vectorStore(t)
			if fts && !s.historyFTS {
				t.Skip("FTS unavailable")
			}
			s.historyFTS = fts
			ctx := context.Background()
			for i := 0; i < 45; i++ {
				e := vectorEvent(int64(i+20), "current", fmt.Sprint(i), "Aurora release Friday")
				if err := s.AppendMessageEvent(ctx, "bot:group:current", e); err != nil {
					t.Fatal(err)
				}
			}
			old := vectorEvent(1, "other", "old", "Aurora release Friday")
			if err := s.AppendMessageEvent(ctx, "bot:group:other", old); err != nil {
				t.Fatal(err)
			}
			got, total, err := s.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{Session: "bot:group:current", SessionPrefix: "bot:group:", ExcludeSession: "bot:group:current", CrossSession: true, Text: "Aurora release Friday", FromTime: 0, ThroughTime: 100, Limit: 4})
			if err != nil || total != 1 || len(got) != 1 || got[0].MessageID != "old" {
				t.Fatalf("current session crowded out old match: %v total=%d err=%v", got, total, err)
			}
		})
	}
}

func TestVectorThresholdAndSessionExclusion(t *testing.T) {
	s := vectorStore(t)
	ctx := context.Background()
	for _, item := range []struct {
		session, id string
		vector      []float32
	}{
		{"bot:group:current", "current", []float32{1, 0}},
		{"bot:group:other", "strong", []float32{1, 0}},
		{"bot:group:other", "weak", []float32{0.1, 1}},
		{"foreign:group:other", "foreign", []float32{1, 0}},
	} {
		if err := s.AppendMessageEvent(ctx, item.session, vectorEvent(1, "other", item.id, "indexed text")); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveMessageEventVector(ctx, item.session, item.id, "embed", item.vector); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SearchMessageEventsByVector(ctx, assistant.MessageHistoryVectorQuery{Session: "bot:group:current", SessionPrefix: "bot:group:", ExcludeSession: "bot:group:current", CrossSession: true, Model: "embed", Vector: []float32{1, 0}, MinSimilarity: 0.65, ThroughTime: 100, Limit: 4})
	if err != nil || len(got) != 1 || got[0].MessageID != "strong" {
		t.Fatalf("vector boundaries: %v err=%v", got, err)
	}
}
