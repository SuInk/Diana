// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPruneLogs(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	debugBefore, otherBefore := now.AddDate(0, 0, -7), now.AddDate(0, 0, -30)
	entries := []AppLogEntry{
		{ID: "debug-old", Kind: LogKindDebug, CreatedAt: debugBefore.Add(-time.Nanosecond)},
		{ID: "debug-boundary", Kind: LogKindDebug, CreatedAt: debugBefore},
		{ID: "debug-fraction", Kind: LogKindDebug, CreatedAt: debugBefore.Add(time.Nanosecond)},
		{ID: "operation-recent", Kind: LogKindOperation, CreatedAt: now.AddDate(0, 0, -8)},
		{ID: "error-old", Kind: LogKindError, CreatedAt: otherBefore.Add(-time.Second)},
		{ID: "error-boundary", Kind: LogKindError, CreatedAt: otherBefore},
		{ID: "offset-recent", Kind: LogKindError, CreatedAt: now.In(time.FixedZone("UTC+8", 8*3600))},
	}
	for _, action := range []string{"assistant.llm_usage", "chatbot.llm_usage", "diana.llm_usage"} {
		entries = append(entries, AppLogEntry{ID: action, Action: action, Kind: LogKindDebug, CreatedAt: now.AddDate(-1, 0, 0)})
	}
	for _, entry := range entries {
		if err := s.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	// Cross more than one batch, and keep non-log state in the same database.
	for i := 0; i < 501; i++ {
		if err := s.AppendLog(ctx, AppLogEntry{ID: fmt.Sprint(i), CreatedAt: otherBefore.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.saveJSON(ctx, "retention-test", "keep"); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.PruneLogs(ctx, debugBefore, otherBefore)
	if err != nil || deleted != 503 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	logs, err := s.ListLogs(ctx, AppLogFilter{Limit: 100})
	if err != nil || len(logs) != 8 {
		t.Fatalf("remaining=%d err=%v", len(logs), err)
	}
	var value string
	if ok, err := s.loadJSON(ctx, "retention-test", &value); err != nil || !ok || value != "keep" {
		t.Fatalf("state changed: %q %v", value, err)
	}
	if n, err := s.PruneLogs(ctx, time.Time{}, time.Time{}); n != 0 || err != nil {
		t.Fatalf("disabled cleanup: %d %v", n, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.PruneLogs(cancelCtx, now, now); err == nil {
		t.Fatal("expected cancellation")
	}
}
