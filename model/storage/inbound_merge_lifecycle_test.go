// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestReplyMergeDurableSettlement(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "delivered"}[final], func(t *testing.T) {
			store := handoffTestStore(t)
			ctx := context.Background()
			event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "user", MessageID: "follow", RawMessage: "再举个例子"}
			id, _, err := store.EnqueueInboundEvent(ctx, "private:user", event)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute)); err != nil || !ok {
				t.Fatalf("claim=%v err=%v", ok, err)
			}
			if err := store.RecordInboundEventReplyMerge(ctx, event, "root-turn"); err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteInboundEvent(ctx, id, "worker", "merged_into_reply"); err != nil {
				t.Fatal(err)
			}
			pending, err := store.ListPendingInboundHandoffs(ctx, 10)
			if err != nil || len(pending) != 1 || pending[0].ID != id {
				t.Fatalf("merged request missing from restart recovery: %#v err=%v", pending, err)
			}
			ref := assistant.InboundHandoffRef{ID: id}
			if final {
				if err := store.FinalizeInboundHandoff(ctx, ref, "root-turn"); err != nil {
					t.Fatal(err)
				}
				status, outcome, _, state, _ := handoffRow(t, store, id)
				if status != inboundStatusDone || outcome != "superseded_follow_up" || state != "final" {
					t.Fatalf("status=%s outcome=%s state=%s", status, outcome, state)
				}
			} else {
				matched, requeued, err := store.ReleaseInboundHandoff(ctx, ref, "root-turn")
				if err != nil || !matched || !requeued {
					t.Fatalf("release matched=%v requeued=%v err=%v", matched, requeued, err)
				}
				status, outcome, by, state, _ := handoffRow(t, store, id)
				if status != inboundStatusPending || outcome != "" || by != "" || state != "" {
					t.Fatalf("status=%s outcome=%s by=%s state=%s", status, outcome, by, state)
				}
			}
		})
	}
}

func TestReplyMergeStorageDoesNotCrossProfiles(t *testing.T) {
	store := handoffTestStore(t)
	ctx := context.Background()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "group", UserID: "user", MessageID: "same-platform-message", RawMessage: "问个问题", ProfileID: "a", SelfID: "bot-a"}
	idA, _, err := store.EnqueueInboundEvent(ctx, "profile:a:group:group", event)
	if err != nil {
		t.Fatal(err)
	}
	other := event
	other.ProfileID = "b"
	other.SelfID = "bot-b"
	idB, _, err := store.EnqueueInboundEvent(ctx, "profile:b:group:group", other)
	if err != nil || idA == idB {
		t.Fatalf("profile enqueue err=%v IDs=%s/%s", err, idA, idB)
	}
	if err := store.RecordInboundEventReplyMerge(ctx, event, "root-a"); err != nil {
		t.Fatal(err)
	}
	_, _, byA, _, _ := handoffRow(t, store, idA)
	_, _, byB, _, _ := handoffRow(t, store, idB)
	if byA != "root-a" || byB != "" {
		t.Fatalf("cross-profile ownership a=%s b=%s", byA, byB)
	}
	if _, superseded, err := store.InboundEventSuperseded(ctx, other); err != nil || superseded {
		t.Fatalf("other profile was blocked: superseded=%v err=%v", superseded, err)
	}
	if matched, _, err := store.ReleaseInboundHandoff(ctx, assistant.InboundHandoffRef{Event: other}, "root-a"); err != nil || matched {
		t.Fatalf("other profile released ownership: matched=%v err=%v", matched, err)
	}
}
