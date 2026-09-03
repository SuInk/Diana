// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

func TestInboundEventDebugTraceIsScopedToExactEvent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "debug-trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	event := assistant.MessageEvent{
		Kind:      assistant.EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-1",
		MessageID: "shared-message-id",
		Time:      time.Now().Unix(),
	}
	eventID, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	for _, entry := range []applog.Entry{
		{
			Kind: applog.KindDebug, Action: "diana.debug_trace", Target: event.MessageID,
			Message: "matching", Metadata: map[string]any{"kind": "group", "group_id": "group-1", "user_id": "user-1", "phase": "model_request"},
		},
		{
			Kind: applog.KindDebug, Action: "diana.debug_trace", Target: event.MessageID,
			Message: "other group", Metadata: map[string]any{"kind": "group", "group_id": "group-2", "user_id": "user-1", "phase": "model_request"},
		},
	} {
		if err := store.AppendLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	messageID, steps, found, err := store.InboundEventDebugTrace(ctx, eventID)
	if err != nil || !found {
		t.Fatalf("trace found=%v err=%v", found, err)
	}
	if messageID != event.MessageID || len(steps) != 1 || steps[0].Message != "matching" {
		t.Fatalf("messageID=%q steps=%#v", messageID, steps)
	}
	if _, _, found, err := store.InboundEventDebugTrace(ctx, "missing"); err != nil || found {
		t.Fatalf("missing found=%v err=%v", found, err)
	}
}
