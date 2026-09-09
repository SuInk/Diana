package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func TestLLMUsageRollingWindow(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	until := time.Date(2026, 9, 9, 12, 30, 0, 500000000, time.UTC)
	since := until.Add(-24 * time.Hour)
	for i, item := range []struct {
		at     time.Time
		action string
	}{
		{since.Add(-time.Nanosecond), "diana.llm_usage"},
		{since, "assistant.llm_usage"},
		{since.Add(time.Hour), "chatbot.llm_usage"},
		{until.Add(-time.Nanosecond), "diana.llm_usage"},
		{until, "diana.llm_usage"},
		{until.Add(time.Nanosecond), "diana.llm_usage"},
		{since.Add(time.Hour), "assistant.agent_tool"},
	} {
		meta := map[string]any{"input_tokens": 100, "output_tokens": 20, "cached_input_tokens": 60}
		if i == 2 {
			meta["total_tokens"] = 130
		}
		if err := s.AppendLog(ctx, applog.Entry{Action: item.action, CreatedAt: item.at, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LLMUsageSince(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if got.Calls != 3 || got.InputTokens != 300 || got.OutputTokens != 60 || got.TotalTokens != 370 || got.CachedInputTokens != 180 {
		t.Fatalf("incorrect totals: %+v", got)
	}
	// A whole-second boundary must still include fractional timestamps in that second.
	got, err = s.LLMUsageSince(ctx, since.Truncate(time.Second), since.Truncate(time.Second).Add(time.Second))
	if err != nil || got.Calls != 2 {
		t.Fatalf("fractional timestamps: %+v, %v", got, err)
	}
	got, err = s.LLMUsageSince(ctx, until.Add(time.Hour), until.Add(2*time.Hour))
	if err != nil || got.Calls != 0 {
		t.Fatalf("empty window: %+v, %v", got, err)
	}
}
