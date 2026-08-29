// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestRecordInboundEventReplyMergeLinksRootTurn(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "reply-merge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m2", RawMessage: "补充"}
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:g1", event)
	if err != nil || !inserted {
		t.Fatalf("enqueue id=%q inserted=%v err=%v", id, inserted, err)
	}
	if err := store.RecordInboundEventReplyMerge(ctx, event, "root-turn"); err != nil {
		t.Fatal(err)
	}
	var linked string
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(superseded_by, '') FROM inbound_events WHERE id = ?`, id).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != "root-turn" {
		t.Fatalf("superseded_by=%q, want root-turn", linked)
	}
}
