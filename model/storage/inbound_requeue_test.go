// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestRequeueFailedInboundEvent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "requeue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	old := time.Now().Add(-48 * time.Hour).Unix()
	enqueueDone := func(messageID, outcome, decision string) string {
		t.Helper()
		id, _, err := store.EnqueueInboundEvent(ctx, "private:1", assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "1", MessageID: messageID, RawMessage: "hi", Time: old})
		if err != nil {
			t.Fatal(err)
		}
		item, ok, err := store.ClaimNextInboundEvent(ctx, "w", time.Now().Add(time.Minute))
		if err != nil || !ok || item.ID != id {
			t.Fatalf("claim = %+v %v %v", item, ok, err)
		}
		if err := store.CompleteInboundEvent(ctx, id, "w", outcome); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET decision = ? WHERE id = ?`, decision, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failed := enqueueDone("m1", "error_silent", "error")
	ignored := enqueueDone("m2", "ignored_policy", "ignored")

	if err := store.RequeueFailedInboundEvent(ctx, ignored); !errors.Is(err, ErrInboundEventNotRetryable) {
		t.Fatalf("requeue non-failed = %v", err)
	}
	if err := store.RequeueFailedInboundEvent(ctx, failed); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "w", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != failed {
		t.Fatalf("claim after requeue = %+v %v %v", item, ok, err)
	}
	if !item.Event.ManualRetry || item.Event.MessageID != "m1" {
		t.Fatalf("requeued event = %+v", item.Event)
	}
	if err := store.CompleteInboundEvent(ctx, failed, "w", "error_silent"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET decision = 'error' WHERE id = ?`, failed); err != nil {
		t.Fatal(err)
	}

	enqueueDone("m3", "error_silent", "error")
	enqueueDone("m4", "error_silent", "error")
	count, err := store.RequeueFailedInboundEvents(ctx, time.Now().Add(-time.Hour), "", 2)
	if err != nil || count != 2 {
		t.Fatalf("bulk requeue with per-session limit = %d %v", count, err)
	}
	var pending []string
	rows, err := store.db.QueryContext(ctx, `SELECT message_id FROM inbound_events WHERE status = 'pending' ORDER BY message_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		pending = append(pending, id)
	}
	if len(pending) != 2 || pending[0] != "m3" || pending[1] != "m4" {
		t.Fatalf("pending = %v, want the two most recent", pending)
	}
}
