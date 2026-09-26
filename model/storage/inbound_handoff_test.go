// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func handoffTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func handoffRow(t *testing.T, store *SQLiteStore, id string) (status, outcome, supersededBy, state string, priority int) {
	t.Helper()
	err := store.db.QueryRow(`
SELECT status, COALESCE(outcome, ''), COALESCE(superseded_by, ''), COALESCE(handoff_state, ''), priority
FROM inbound_events WHERE id = ?`, id).Scan(&status, &outcome, &supersededBy, &state, &priority)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// 交出去的那一轮收尾后，接手那一轮没回出去：撤销交接把它放回队列、提高优先级；
// 巡检能列出待定的交接（重启恢复靠它）。
func TestInboundHandoffReleaseRequeuesHandedOffEvent(t *testing.T) {
	store := handoffTestStore(t)
	ctx := context.Background()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "12345", UserID: "10001", MessageID: "20001", Time: time.Now().Unix(), RawMessage: "帮我看看"}
	id, _, err := store.EnqueueInboundEvent(ctx, "group:12345", event)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 2}); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.MarkInboundHandoff(ctx, event, "absorber-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteInboundHandoff(ctx, id, "worker", "absorber-1", false); err != nil {
		t.Fatal(err)
	}
	status, outcome, by, state, _ := handoffRow(t, store, id)
	if status != inboundStatusDone || outcome != "handed_off_pending" || by != "absorber-1" || state != "pending" {
		t.Fatalf("after handoff: status=%s outcome=%s by=%s state=%s", status, outcome, by, state)
	}
	pending, err := store.ListPendingInboundHandoffs(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].AbsorberID != "absorber-1" || pending[0].Event.MessageID != "20001" {
		t.Fatalf("pending handoffs=%#v err=%v", pending, err)
	}
	// 别的接手轮次撤销不了这条交接。
	if requeued, err := store.ReleaseInboundHandoff(ctx, event, "someone-else"); err != nil || requeued {
		t.Fatalf("foreign release requeued=%v err=%v", requeued, err)
	}
	requeued, err := store.ReleaseInboundHandoff(ctx, event, "absorber-1")
	if err != nil || !requeued {
		t.Fatalf("release requeued=%v err=%v", requeued, err)
	}
	status, outcome, by, state, priority := handoffRow(t, store, id)
	if status != inboundStatusPending || outcome != "" || by != "" || state != "" || priority < assistant.InboundPriorityTriggered {
		t.Fatalf("after release: status=%s outcome=%s by=%s state=%s priority=%d", status, outcome, by, state, priority)
	}
	if _, superseded, _ := store.InboundEventSuperseded(ctx, event); superseded {
		t.Fatal("released event must pass the pre-send supersession check again")
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 2})
	if err != nil || !ok || item.ID != id {
		t.Fatalf("requeued event not claimable: ok=%v err=%v item=%#v", ok, err, item)
	}
}

// 接手那一轮回出去了：交接落定，结果写成 superseded_follow_up，巡检不再列出它。
func TestInboundHandoffFinalize(t *testing.T) {
	store := handoffTestStore(t)
	ctx := context.Background()
	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "10002", MessageID: "20002", Time: time.Now().Unix(), RawMessage: "在吗"}
	id, _, err := store.EnqueueInboundEvent(ctx, "private:10002", event)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 2}); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.CompleteInboundHandoff(ctx, id, "worker", "absorber-2", false); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeInboundHandoff(ctx, event, "absorber-2"); err != nil {
		t.Fatal(err)
	}
	status, outcome, by, state, _ := handoffRow(t, store, id)
	if status != inboundStatusDone || outcome != "superseded_follow_up" || by != "absorber-2" || state != "final" {
		t.Fatalf("after finalize: status=%s outcome=%s by=%s state=%s", status, outcome, by, state)
	}
	if pending, _ := store.ListPendingInboundHandoffs(ctx, 10); len(pending) != 0 {
		t.Fatalf("finalized handoff still listed: %#v", pending)
	}
	if requeued, _ := store.ReleaseInboundHandoff(ctx, event, "absorber-2"); requeued {
		t.Fatal("a finalized handoff must never be requeued")
	}
}

// 撤销赶在交出去那一轮收尾之前：只清交接列，那一轮还在处理，它自己会接着回答。
func TestInboundHandoffReleaseWhileProcessingOnlyClears(t *testing.T) {
	store := handoffTestStore(t)
	ctx := context.Background()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "12345", UserID: "10003", MessageID: "20003", Time: time.Now().Unix(), RawMessage: "问一下"}
	id, _, err := store.EnqueueInboundEvent(ctx, "group:12345", event)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 2}); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.MarkInboundHandoff(ctx, event, "absorber-3"); err != nil {
		t.Fatal(err)
	}
	if requeued, err := store.ReleaseInboundHandoff(ctx, event, "absorber-3"); err != nil || requeued {
		t.Fatalf("release while processing requeued=%v err=%v", requeued, err)
	}
	status, _, by, state, _ := handoffRow(t, store, id)
	if status != inboundStatusProcessing || by != "" || state != "" {
		t.Fatalf("status=%s by=%s state=%s", status, by, state)
	}
}
