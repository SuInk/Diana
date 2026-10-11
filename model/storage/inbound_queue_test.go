// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestInboundQueuePersistsAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "inbound.db")
	store := openInboundTestStore(t, dbPath)

	legacy := inboundTestEvent("legacy", "old history", 10)
	if err := store.AppendMessageEvent(ctx, "group:100", legacy); err != nil {
		t.Fatal(err)
	}
	if count := pendingInboundCount(t, store); count != 0 {
		t.Fatalf("legacy history migrated into queue: count=%d", count)
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:100", legacy); err != nil || inserted {
		t.Fatalf("legacy event enqueue inserted=%v err=%v", inserted, err)
	}
	if count := pendingInboundCount(t, store); count != 0 {
		t.Fatalf("legacy history entered the reply queue: count=%d", count)
	}

	event := inboundTestEvent("message-1", "hello", 20)
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:100", event)
	if err != nil || !inserted {
		t.Fatalf("first enqueue id=%q inserted=%v err=%v", id, inserted, err)
	}
	duplicateID, inserted, err := store.EnqueueInboundEvent(ctx, "group:100", event)
	if err != nil || inserted || duplicateID != id {
		t.Fatalf("duplicate enqueue id=%q inserted=%v err=%v", duplicateID, inserted, err)
	}
	if count := pendingInboundCount(t, store); count != 1 {
		t.Fatalf("pending count=%d, want 1", count)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openInboundTestStore(t, dbPath)
	defer func() { _ = store.Close() }()
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-1", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if item.ID != id || item.Session != "group:100" || item.Event.MessageID != event.MessageID || item.Attempts != 1 {
		t.Fatalf("claimed item=%#v", item)
	}
	if count := pendingInboundCount(t, store); count != 1 {
		t.Fatalf("processing item must remain outstanding: count=%d", count)
	}
	if err := store.CompleteInboundEvent(ctx, id, "wrong-worker", "ignored"); err == nil {
		t.Fatal("completion with the wrong lease owner unexpectedly succeeded")
	}
	if err := store.CompleteInboundEvent(ctx, id, "worker-1", "handled"); err != nil {
		t.Fatal(err)
	}
	if count := pendingInboundCount(t, store); count != 0 {
		t.Fatalf("pending count after completion=%d", count)
	}

	var status, outcome string
	if err := store.db.QueryRowContext(ctx, `SELECT status, outcome FROM inbound_events WHERE id = ?`, id).Scan(&status, &outcome); err != nil {
		t.Fatal(err)
	}
	if status != inboundStatusDone || outcome != "handled" {
		t.Fatalf("terminal state status=%q outcome=%q", status, outcome)
	}
}

func TestInboundQueueDeduplicatesAcrossContextNamespaces(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "namespace-dedupe.db"))
	defer func() { _ = store.Close() }()
	event := inboundTestEvent("same-onebot-message", "only once", time.Now().Unix())
	event.Platform = assistant.PlatformOneBotV11
	event.SelfID = "bot-1"
	event.GroupID = "group-1"
	event.UserID = "user-1"
	event.ContextNamespace = "old-profile"
	firstID, inserted, err := store.EnqueueInboundEvent(ctx, "old-profile:group:group-1", event)
	if err != nil || !inserted {
		t.Fatalf("first enqueue id=%q inserted=%v err=%v", firstID, inserted, err)
	}
	event.ContextNamespace = "new-profile"
	secondID, inserted, err := store.EnqueueInboundEvent(ctx, "new-profile:group:group-1", event)
	if err != nil || inserted || secondID != firstID {
		t.Fatalf("namespace duplicate id=%q inserted=%v err=%v, first=%q", secondID, inserted, err, firstID)
	}
	var inboundRows, historyRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM inbound_events WHERE message_id = ?`, event.MessageID).Scan(&inboundRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM message_events WHERE message_id = ?`, event.MessageID).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if inboundRows != 1 || historyRows != 1 {
		t.Fatalf("deduplicated rows inbound=%d history=%d", inboundRows, historyRows)
	}
}

func TestInboundQueueEditRefreshesPendingHistoryAndPayload(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "pending-edit.db"))
	defer func() { _ = store.Close() }()

	original := telegramInboundTestEvent("message-edit-pending", "before edit", 100)
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", original)
	if err != nil || !inserted {
		t.Fatalf("enqueue original id=%q inserted=%v err=%v", id, inserted, err)
	}

	edited := telegramInboundTestEvent("message-edit-pending", "after edit", 100)
	edited.Edited = true
	edited.EditTime = 150
	editedID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", edited)
	if err != nil || inserted || editedID != id {
		t.Fatalf("enqueue edit id=%q inserted=%v err=%v, want existing id=%q", editedID, inserted, err, id)
	}
	if count := pendingInboundCount(t, store); count != 1 {
		t.Fatalf("pending count=%d, want 1", count)
	}

	history, ok, err := store.FindMessageEvent(ctx, "group:1", original.MessageID)
	if err != nil || !ok {
		t.Fatalf("find edited history ok=%v err=%v", ok, err)
	}
	assertEditedInboundEvent(t, history, "after edit", 150)

	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-edit", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim edited pending event ok=%v err=%v", ok, err)
	}
	if item.ID != id {
		t.Fatalf("claimed id=%q, want %q", item.ID, id)
	}
	assertEditedInboundEvent(t, item.Event, "after edit", 150)
}

func TestInboundQueueEditRefreshesCompletedHistoryWithoutReopening(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "completed-edit.db"))
	defer func() { _ = store.Close() }()

	original := telegramInboundTestEvent("message-edit-done", "before edit", 200)
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", original)
	if err != nil || !inserted {
		t.Fatalf("enqueue original id=%q inserted=%v err=%v", id, inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-done", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != id {
		t.Fatalf("claim original item=%#v ok=%v err=%v", item, ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, id, "worker-done", "handled"); err != nil {
		t.Fatal(err)
	}

	edited := telegramInboundTestEvent("message-edit-done", "after completion", 200)
	edited.Edited = true
	edited.EditTime = 260
	editedID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", edited)
	if err != nil || inserted || editedID != id {
		t.Fatalf("enqueue completed edit id=%q inserted=%v err=%v, want existing id=%q", editedID, inserted, err, id)
	}
	if count := pendingInboundCount(t, store); count != 0 {
		t.Fatalf("completed edit reopened queue: pending count=%d", count)
	}
	if next, ok, err := store.ClaimNextInboundEvent(ctx, "worker-next", time.Now().Add(time.Minute)); err != nil || ok {
		t.Fatalf("completed edit became claimable: item=%#v ok=%v err=%v", next, ok, err)
	}

	history, ok, err := store.FindMessageEvent(ctx, "group:1", original.MessageID)
	if err != nil || !ok {
		t.Fatalf("find edited history ok=%v err=%v", ok, err)
	}
	assertEditedInboundEvent(t, history, "after completion", 260)

	var status, outcome, queuePayload string
	var attempts int
	if err := store.db.QueryRowContext(ctx, `SELECT status, outcome, attempts, payload FROM inbound_events WHERE id = ?`, id).Scan(&status, &outcome, &attempts, &queuePayload); err != nil {
		t.Fatal(err)
	}
	if status != inboundStatusDone || outcome != "handled" || attempts != 1 {
		t.Fatalf("completed queue state changed: status=%q outcome=%q attempts=%d", status, outcome, attempts)
	}
	var queued assistant.MessageEvent
	if err := json.Unmarshal([]byte(queuePayload), &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Edited || queued.RawMessage != "before edit" {
		t.Fatalf("completed queue payload changed after history-only edit: %+v", queued)
	}
}

func TestInboundQueueAcceptsEditAsFirstSeenMessage(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "first-seen-edit.db"))
	defer func() { _ = store.Close() }()

	edited := telegramInboundTestEvent("message-first-seen-edit", "first seen after edit", 300)
	edited.Edited = true
	edited.EditTime = 350
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", edited)
	if err != nil || !inserted {
		t.Fatalf("first-seen edit id=%q inserted=%v err=%v", id, inserted, err)
	}
	if count := pendingInboundCount(t, store); count != 1 {
		t.Fatalf("pending count=%d, want 1", count)
	}

	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-first-seen", time.Now().Add(time.Minute))
	if err != nil || !ok || item.ID != id {
		t.Fatalf("claim first-seen edit item=%#v ok=%v err=%v", item, ok, err)
	}
	assertEditedInboundEvent(t, item.Event, "first seen after edit", 350)
}

func TestAppendMessageEventCannotRestoreClaimedSnapshotAfterEdit(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "processing-edit.db"))
	defer func() { _ = store.Close() }()

	session := "group:1"
	original := telegramInboundTestEvent("message-edit-processing", "worker snapshot", 400)
	id, inserted, err := store.EnqueueInboundEvent(ctx, session, original)
	if err != nil || !inserted {
		t.Fatalf("enqueue original id=%q inserted=%v err=%v", id, inserted, err)
	}
	claimed, ok, err := store.ClaimNextInboundEvent(ctx, "worker-processing", time.Now().Add(time.Minute))
	if err != nil || !ok || claimed.ID != id {
		t.Fatalf("claim original item=%#v ok=%v err=%v", claimed, ok, err)
	}

	edited := telegramInboundTestEvent(original.MessageID, "current edited text", original.Time)
	edited.Edited = true
	edited.EditTime = 450
	refresh, err := store.RefreshInboundMessageEdit(ctx, session, edited)
	if err != nil || !refresh.Found || !refresh.Applied || refresh.EventID != id {
		t.Fatalf("refresh edit=%+v err=%v", refresh, err)
	}
	// 模拟已经持有旧 processing 快照的 worker 完成预处理后再次记历史。
	if err := store.AppendMessageEvent(ctx, session, claimed.Event); err != nil {
		t.Fatal(err)
	}

	history, found, err := store.FindMessageEvent(ctx, session, original.MessageID)
	if err != nil || !found {
		t.Fatalf("find history found=%v err=%v", found, err)
	}
	assertEditedInboundEvent(t, history, "current edited text", 450)

	var status, queuePayload string
	if err := store.db.QueryRowContext(ctx, `SELECT status, payload FROM inbound_events WHERE id = ?`, id).Scan(&status, &queuePayload); err != nil {
		t.Fatal(err)
	}
	var queued assistant.MessageEvent
	if err := json.Unmarshal([]byte(queuePayload), &queued); err != nil {
		t.Fatal(err)
	}
	if status != inboundStatusProcessing || queued.Edited || queued.RawMessage != "worker snapshot" {
		t.Fatalf("processing snapshot changed after edit: status=%q event=%+v", status, queued)
	}
}

func TestInboundQueueOrdersEditedRevisionsByEditTime(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "edit-order.db"))
	defer func() { _ = store.Close() }()

	session := "group:1"
	original := telegramInboundTestEvent("message-edit-order", "original", 500)
	if _, inserted, err := store.EnqueueInboundEvent(ctx, session, original); err != nil || !inserted {
		t.Fatalf("enqueue original inserted=%v err=%v", inserted, err)
	}

	edit := func(text string, editTime int64) assistant.MessageEvent {
		event := telegramInboundTestEvent(original.MessageID, text, original.Time)
		event.Edited = true
		event.EditTime = editTime
		return event
	}
	latest := edit("newer revision", 560)
	refresh, err := store.RefreshInboundMessageEdit(ctx, session, latest)
	if err != nil || !refresh.Found || !refresh.Applied {
		t.Fatalf("apply latest refresh=%+v err=%v", refresh, err)
	}
	stale := edit("stale revision", 559)
	refresh, err = store.RefreshInboundMessageEdit(ctx, session, stale)
	if err != nil || !refresh.Found || refresh.Applied {
		t.Fatalf("stale refresh=%+v err=%v, want found but ignored", refresh, err)
	}
	history, found, err := store.FindMessageEvent(ctx, session, original.MessageID)
	if err != nil || !found {
		t.Fatalf("find after stale edit found=%v err=%v", found, err)
	}
	assertEditedInboundEvent(t, history, "newer revision", 560)

	// Telegram 的 edit_date 只有秒级精度，同一秒后到的 revision 仍要覆盖。
	sameSecond := edit("same-second later arrival", 560)
	refresh, err = store.RefreshInboundMessageEdit(ctx, session, sameSecond)
	if err != nil || !refresh.Found || !refresh.Applied {
		t.Fatalf("same-second refresh=%+v err=%v, want applied", refresh, err)
	}
	history, found, err = store.FindMessageEvent(ctx, session, original.MessageID)
	if err != nil || !found {
		t.Fatalf("find same-second edit found=%v err=%v", found, err)
	}
	assertEditedInboundEvent(t, history, "same-second later arrival", 560)
}

func TestInboundQueueOrdersSameSecondEditsByTelegramUpdateID(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "edit-update-order.db"))
	defer func() { _ = store.Close() }()

	session := "group:1"
	original := telegramInboundTestEvent("message-edit-update-order", "original", 500)
	if _, inserted, err := store.EnqueueInboundEvent(ctx, session, original); err != nil || !inserted {
		t.Fatalf("enqueue original inserted=%v err=%v", inserted, err)
	}
	edit := func(text string, updateID int64) assistant.MessageEvent {
		event := telegramInboundTestEvent(original.MessageID, text, original.Time)
		event.Edited = true
		event.EditTime = 560
		event.TelegramUpdateID = updateID
		return event
	}

	first := edit("first same-second revision", 100)
	firstRefresh, err := store.RefreshInboundMessageEdit(ctx, session, first)
	if err != nil || !firstRefresh.Applied {
		t.Fatalf("apply first edit refresh=%+v err=%v", firstRefresh, err)
	}
	// Inbound redelivery of the same update is a duplicate, while the worker that
	// claimed that exact durable revision may still append its derived fields.
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, first); err != nil || !refresh.Found || refresh.Applied {
		t.Fatalf("same update retry refresh=%+v err=%v, want ignored", refresh, err)
	}
	workerRevision := firstRefresh.Event
	workerRevision.SenderName = "enriched sender"
	if durable, written, err := store.AppendMessageEventRevision(ctx, session, workerRevision); err != nil || !written || durable.SenderName != workerRevision.SenderName {
		t.Fatalf("same revision worker append durable=%+v written=%v err=%v", durable, written, err)
	}
	latest := edit("latest same-second revision", 101)
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, latest); err != nil || !refresh.Applied {
		t.Fatalf("apply latest edit refresh=%+v err=%v", refresh, err)
	}
	stale := edit("stale same-second revision", 100)
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, stale); err != nil || !refresh.Found || refresh.Applied {
		t.Fatalf("stale same-second refresh=%+v err=%v, want found but ignored", refresh, err)
	}

	current, found, err := store.FindMessageEvent(ctx, session, original.MessageID)
	if err != nil || !found {
		t.Fatalf("find latest same-second edit found=%v err=%v", found, err)
	}
	if current.RawMessage != latest.RawMessage || current.TelegramUpdateID != latest.TelegramUpdateID {
		t.Fatalf("current revision=%+v, want update_id=%d text=%q", current, latest.TelegramUpdateID, latest.RawMessage)
	}

	// A worker that claimed either the original payload or an older edit before
	// update 101 arrived must receive the durable revision instead of overwriting it.
	for _, workerSnapshot := range []assistant.MessageEvent{stale, original} {
		durable, written, err := store.AppendMessageEventRevision(ctx, session, workerSnapshot)
		if err != nil {
			t.Fatalf("append worker snapshot update_id=%d: %v", workerSnapshot.TelegramUpdateID, err)
		}
		if written {
			t.Fatalf("stale worker snapshot update_id=%d overwrote the durable edit", workerSnapshot.TelegramUpdateID)
		}
		if durable.RawMessage != latest.RawMessage || durable.TelegramUpdateID != latest.TelegramUpdateID {
			t.Fatalf("durable revision=%+v, want update_id=%d text=%q", durable, latest.TelegramUpdateID, latest.RawMessage)
		}
	}
}

func TestInboundQueueEditTimeOutranksTelegramUpdateIDAcrossSeconds(t *testing.T) {
	operations := []struct {
		name  string
		apply func(context.Context, *SQLiteStore, string, assistant.MessageEvent) (assistant.MessageEvent, bool, error)
	}{
		{
			name: "inbound refresh",
			apply: func(ctx context.Context, store *SQLiteStore, session string, event assistant.MessageEvent) (assistant.MessageEvent, bool, error) {
				refresh, err := store.RefreshInboundMessageEdit(ctx, session, event)
				return refresh.Event, refresh.Applied, err
			},
		},
		{
			name: "worker append",
			apply: func(ctx context.Context, store *SQLiteStore, session string, event assistant.MessageEvent) (assistant.MessageEvent, bool, error) {
				return store.AppendMessageEventRevision(ctx, session, event)
			},
		},
	}
	cases := []struct {
		name       string
		editTime   int64
		updateID   int64
		wantApply  bool
		wantText   string
		wantTime   int64
		wantUpdate int64
	}{
		{
			name:       "older edit with higher update id is stale",
			editTime:   749,
			updateID:   999,
			wantText:   "current revision",
			wantTime:   750,
			wantUpdate: 500,
		},
		{
			name:       "newer edit with lower update id wins",
			editTime:   751,
			updateID:   1,
			wantApply:  true,
			wantText:   "candidate revision",
			wantTime:   751,
			wantUpdate: 1,
		},
	}

	for _, operation := range operations {
		for _, test := range cases {
			t.Run(operation.name+"/"+test.name, func(t *testing.T) {
				ctx := context.Background()
				store := openInboundTestStore(t, filepath.Join(t.TempDir(), "edit-time-order.db"))
				defer func() { _ = store.Close() }()

				session := "group:1"
				original := telegramInboundTestEvent("message-edit-time-order", "original", 700)
				if _, inserted, err := store.EnqueueInboundEvent(ctx, session, original); err != nil || !inserted {
					t.Fatalf("enqueue original inserted=%v err=%v", inserted, err)
				}
				current := telegramInboundTestEvent(original.MessageID, "current revision", original.Time)
				current.Edited = true
				current.EditTime = 750
				current.TelegramUpdateID = 500
				if refresh, err := store.RefreshInboundMessageEdit(ctx, session, current); err != nil || !refresh.Applied {
					t.Fatalf("apply current edit refresh=%+v err=%v", refresh, err)
				}

				candidate := telegramInboundTestEvent(original.MessageID, "candidate revision", original.Time)
				candidate.Edited = true
				candidate.EditTime = test.editTime
				candidate.TelegramUpdateID = test.updateID
				got, applied, err := operation.apply(ctx, store, session, candidate)
				if err != nil {
					t.Fatal(err)
				}
				if applied != test.wantApply {
					t.Fatalf("applied=%v, want %v; revision=%+v", applied, test.wantApply, got)
				}
				if got.RawMessage != test.wantText || got.EditTime != test.wantTime || got.TelegramUpdateID != test.wantUpdate {
					t.Fatalf("revision=%+v, want text=%q edit_time=%d update_id=%d", got, test.wantText, test.wantTime, test.wantUpdate)
				}
			})
		}
	}
}

func TestInboundQueueSameEditedUpdateRetryKeepsFirstSeenSemantics(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "first-seen-edit-retry.db"))
	defer func() { _ = store.Close() }()

	session := "group:1"
	firstSeen := telegramInboundTestEvent("first-seen-edit-retry", "first visible revision", 600)
	firstSeen.Edited = true
	firstSeen.EditTime = 650
	firstSeen.TelegramUpdateID = 200
	if _, inserted, err := store.EnqueueInboundEvent(ctx, session, firstSeen); err != nil || !inserted {
		t.Fatalf("enqueue first-seen edit inserted=%v err=%v", inserted, err)
	}
	refresh, err := store.RefreshInboundMessageEdit(ctx, session, firstSeen)
	if err != nil || !refresh.Found || refresh.Applied {
		t.Fatalf("retry refresh=%+v err=%v, want known duplicate without apply", refresh, err)
	}
	if refresh.Event.EditRefreshOnly {
		t.Fatalf("same update retry changed first-seen edit semantics: %+v", refresh.Event)
	}
	current, found, err := store.FindMessageEvent(ctx, session, firstSeen.MessageID)
	if err != nil || !found {
		t.Fatalf("find first-seen edit found=%v err=%v", found, err)
	}
	if current.EditRefreshOnly || current.TelegramUpdateID != firstSeen.TelegramUpdateID {
		t.Fatalf("stored first-seen edit changed after retry: %+v", current)
	}
}

func TestInboundQueueRejectsSameSecondLegacyEditAfterNumberedRevision(t *testing.T) {
	for _, test := range []struct {
		name  string
		apply func(context.Context, *SQLiteStore, string, assistant.MessageEvent) (assistant.MessageEvent, bool, error)
	}{
		{
			name: "inbound refresh",
			apply: func(ctx context.Context, store *SQLiteStore, session string, stale assistant.MessageEvent) (assistant.MessageEvent, bool, error) {
				refresh, err := store.RefreshInboundMessageEdit(ctx, session, stale)
				return refresh.Event, refresh.Applied, err
			},
		},
		{
			name: "worker append",
			apply: func(ctx context.Context, store *SQLiteStore, session string, stale assistant.MessageEvent) (assistant.MessageEvent, bool, error) {
				return store.AppendMessageEventRevision(ctx, session, stale)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openInboundTestStore(t, filepath.Join(t.TempDir(), "legacy-edit-order.db"))
			defer func() { _ = store.Close() }()

			session := "group:1"
			original := telegramInboundTestEvent("legacy-edit-order", "original", 700)
			if _, inserted, err := store.EnqueueInboundEvent(ctx, session, original); err != nil || !inserted {
				t.Fatalf("enqueue original inserted=%v err=%v", inserted, err)
			}
			latest := telegramInboundTestEvent(original.MessageID, "numbered current revision", original.Time)
			latest.Edited = true
			latest.EditTime = 750
			latest.TelegramUpdateID = 301
			if refresh, err := store.RefreshInboundMessageEdit(ctx, session, latest); err != nil || !refresh.Applied {
				t.Fatalf("apply numbered edit refresh=%+v err=%v", refresh, err)
			}

			stale := telegramInboundTestEvent(original.MessageID, "legacy same-second revision", original.Time)
			stale.Edited = true
			stale.EditTime = latest.EditTime
			current, applied, err := test.apply(ctx, store, session, stale)
			if err != nil {
				t.Fatal(err)
			}
			if applied {
				t.Fatal("same-second edit without update_id overwrote a numbered Telegram revision")
			}
			if current.RawMessage != latest.RawMessage || current.TelegramUpdateID != latest.TelegramUpdateID {
				t.Fatalf("current revision=%+v, want update_id=%d text=%q", current, latest.TelegramUpdateID, latest.RawMessage)
			}
		})
	}
}

func TestInboundQueueEditPreservesSearchExtraOnlyForSameMedia(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "edit-media.db"))
	defer func() { _ = store.Close() }()

	session := "group:1"
	mediaEvent := func(text, uniqueID, fileID string, editTime int64, derived bool) assistant.MessageEvent {
		event := telegramInboundTestEvent("message-edit-media", text, 600)
		event.Segments = []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}
		if uniqueID != "" {
			data := map[string]string{"file_unique_id": uniqueID, "file_id": fileID}
			if derived {
				data["cached_file"] = "/cache/old-photo.jpg"
				data["summary"] = "旧的图片摘要"
				data["description"] = "旧的视觉描述"
			}
			event.Segments = append(event.Segments, assistant.MessageSegment{Type: "image", Data: data})
		}
		if editTime > 0 {
			event.Edited = true
			event.EditTime = editTime
		}
		return event
	}
	original := mediaEvent("旧 caption", "photo-unique-a", "photo-file-a", 0, true)
	id, inserted, err := store.EnqueueInboundEvent(ctx, session, original)
	if err != nil || !inserted {
		t.Fatalf("enqueue media id=%q inserted=%v err=%v", id, inserted, err)
	}
	if err := store.SaveMessageSearchExtra(ctx, session, original.MessageID, "穿红围巾的橘猫"); err != nil {
		t.Fatal(err)
	}
	searchExtra := func() string {
		t.Helper()
		var extra string
		if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(search_extra, '') FROM message_events WHERE id = ?`, id).Scan(&extra); err != nil {
			t.Fatal(err)
		}
		return extra
	}

	// file_id 会轮换，旧 payload 还带 cached_file/summary/description；稳定的
	// file_unique_id 相同就仍是同一张图，caption 编辑应保留视觉检索文本。
	captionEdit := mediaEvent("新 caption", "photo-unique-a", "photo-file-a-rotated", 650, false)
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, captionEdit); err != nil || !refresh.Applied {
		t.Fatalf("caption edit refresh=%+v err=%v", refresh, err)
	}
	if got := searchExtra(); got != "穿红围巾的橘猫" {
		t.Fatalf("same-media caption edit search_extra=%q", got)
	}

	replaced := mediaEvent("换图", "photo-unique-b", "photo-file-b", 651, false)
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, replaced); err != nil || !refresh.Applied {
		t.Fatalf("replacement edit refresh=%+v err=%v", refresh, err)
	}
	if got := searchExtra(); got != "" {
		t.Fatalf("replaced media kept stale search_extra=%q", got)
	}

	if err := store.SaveMessageSearchExtra(ctx, session, original.MessageID, "蓝色海边的新图片"); err != nil {
		t.Fatal(err)
	}
	removed := mediaEvent("图片已删除", "", "", 652, false)
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, removed); err != nil || !refresh.Applied {
		t.Fatalf("media removal refresh=%+v err=%v", refresh, err)
	}
	if got := searchExtra(); got != "" {
		t.Fatalf("removed media kept stale search_extra=%q", got)
	}
}

func TestInboundQueueEditPreservesVoiceTranscriptOnlyForSameTelegramMedia(t *testing.T) {
	for _, test := range []struct {
		name     string
		mime     string
		fileName string
	}{
		{name: "voice", mime: "audio/ogg", fileName: "voice.oga"},
		{name: "audio", mime: "audio/mpeg", fileName: "track.mp3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openInboundTestStore(t, filepath.Join(t.TempDir(), "edit-audio.db"))
			defer func() { _ = store.Close() }()

			session := "group:1"
			mediaEvent := func(text, uniqueID, fileID string, editTime int64, derived bool) assistant.MessageEvent {
				event := telegramInboundTestEvent("message-edit-audio-"+test.name, text, 625)
				event.Segments = []assistant.MessageSegment{
					{Type: "text", Data: map[string]string{"text": text}},
					{Type: "record", Data: map[string]string{
						"file_unique_id": uniqueID,
						"file_id":        fileID,
						"name":           test.fileName,
						"mime":           test.mime,
					}},
				}
				if derived {
					data := event.Segments[1].Data
					data["transcript"] = "今晚八点开会"
					data["audio_sha256"] = "audio-hash-a"
					data["stt_backend"] = "openai-compatible"
					data["stt_model"] = "whisper-large-v3"
				}
				if editTime > 0 {
					event.Edited = true
					event.EditTime = editTime
				}
				return event
			}
			findRecord := func() assistant.MessageSegment {
				t.Helper()
				event, found, err := store.FindMessageEvent(ctx, session, "message-edit-audio-"+test.name)
				if err != nil || !found {
					t.Fatalf("find edited audio history found=%v err=%v", found, err)
				}
				for _, segment := range event.Segments {
					if segment.Type == "record" {
						return segment
					}
				}
				t.Fatal("edited audio history has no record segment")
				return assistant.MessageSegment{}
			}

			original := mediaEvent("旧 caption", "audio-unique-a", "audio-file-a", 0, true)
			if _, inserted, err := store.EnqueueInboundEvent(ctx, session, original); err != nil || !inserted {
				t.Fatalf("enqueue original inserted=%v err=%v", inserted, err)
			}

			captionEdit := mediaEvent("新 caption", "audio-unique-a", "audio-file-a-rotated", 675, false)
			if refresh, err := store.RefreshInboundMessageEdit(ctx, session, captionEdit); err != nil || !refresh.Applied {
				t.Fatalf("caption edit refresh=%+v err=%v", refresh, err)
			}
			preserved := findRecord().Data
			for key, want := range map[string]string{
				"transcript":   "今晚八点开会",
				"audio_sha256": "audio-hash-a",
				"stt_backend":  "openai-compatible",
				"stt_model":    "whisper-large-v3",
			} {
				if got := preserved[key]; got != want {
					t.Errorf("same-media caption edit %s=%q, want %q", key, got, want)
				}
			}

			replaced := mediaEvent("换音频", "audio-unique-b", "audio-file-b", 676, false)
			if refresh, err := store.RefreshInboundMessageEdit(ctx, session, replaced); err != nil || !refresh.Applied {
				t.Fatalf("replacement edit refresh=%+v err=%v", refresh, err)
			}
			cleared := findRecord().Data
			for _, key := range []string{"transcript", "audio_sha256", "stt_backend", "stt_model"} {
				if got := strings.TrimSpace(cleared[key]); got != "" {
					t.Errorf("replaced media kept stale %s=%q", key, got)
				}
			}
		})
	}
}

func TestInboundQueueEditUpdatesLegacyNamespaceRowInPlace(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "legacy-edit.db"))
	defer func() { _ = store.Close() }()

	legacySession := "group:group-1"
	currentSession := "profile-current:group:group-1"
	original := telegramInboundTestEvent("message-legacy-edit", "legacy text", 700)
	original.GroupID = "group-1"
	original.UserID = "user-1"
	id, inserted, err := store.EnqueueInboundEvent(ctx, legacySession, original)
	if err != nil || !inserted {
		t.Fatalf("enqueue legacy id=%q inserted=%v err=%v", id, inserted, err)
	}

	edited := telegramInboundTestEvent(original.MessageID, "current namespace edit", original.Time)
	edited.GroupID = original.GroupID
	edited.UserID = original.UserID
	edited.ProfileID = "profile-current"
	edited.ContextNamespace = "profile-current"
	edited.Edited = true
	edited.EditTime = 750
	refresh, err := store.RefreshInboundMessageEdit(ctx, currentSession, edited)
	if err != nil || !refresh.Found || !refresh.Applied || refresh.EventID != id || refresh.Session != legacySession {
		t.Fatalf("legacy refresh=%+v err=%v, want id=%q session=%q", refresh, err, id, legacySession)
	}

	var historyRows, inboundRows int
	var storedSession string
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(session) FROM message_events WHERE message_id = ?`, original.MessageID).Scan(&historyRows, &storedSession); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbound_events WHERE message_id = ?`, original.MessageID).Scan(&inboundRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 1 || inboundRows != 1 || storedSession != legacySession {
		t.Fatalf("edit duplicated legacy row: history=%d inbound=%d session=%q", historyRows, inboundRows, storedSession)
	}
	history, found, err := store.FindMessageEvent(ctx, legacySession, original.MessageID)
	if err != nil || !found {
		t.Fatalf("find legacy history found=%v err=%v", found, err)
	}
	assertEditedInboundEvent(t, history, "current namespace edit", 750)
	if _, found, err := store.FindMessageEvent(ctx, currentSession, original.MessageID); err != nil || found {
		t.Fatalf("edit created current namespace row found=%v err=%v", found, err)
	}
}

func TestInboundRecoveryCheckpointPersists(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "recovery-checkpoint.db"))
	defer func() { _ = store.Close() }()

	if _, ok, err := store.LoadInboundRecoveryCheckpoint(ctx); err != nil || ok {
		t.Fatalf("empty checkpoint ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, time.August, 15, 12, 34, 56, 789, time.UTC)
	if err := store.SaveInboundRecoveryCheckpoint(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LoadInboundRecoveryCheckpoint(ctx)
	if err != nil || !ok || !got.Equal(want) {
		t.Fatalf("checkpoint=%s ok=%v err=%v, want %s", got, ok, err, want)
	}
}

func TestInboundQueueUsesStableHashWithoutMessageID(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "hash.db"))
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{
		Kind:       assistant.EventKindGroup,
		GroupID:    "200",
		UserID:     "300",
		Time:       123,
		RawMessage: "message without an id",
		Segments: []assistant.MessageSegment{{
			Type: "text",
			Data: map[string]string{"text": "message without an id"},
		}},
	}
	id, inserted, err := store.EnqueueInboundEvent(ctx, "group:200", event)
	if err != nil || !inserted {
		t.Fatalf("first enqueue id=%q inserted=%v err=%v", id, inserted, err)
	}
	if !strings.HasPrefix(id, "sha256:") || len(id) != len("sha256:")+64 {
		t.Fatalf("stable fallback id=%q", id)
	}
	idAgain, inserted, err := store.EnqueueInboundEvent(ctx, "group:200", event)
	if err != nil || inserted || idAgain != id {
		t.Fatalf("second enqueue id=%q inserted=%v err=%v", idAgain, inserted, err)
	}
	event.SenderName = "enriched later"
	event.ToMe = true
	idEnriched, inserted, err := store.EnqueueInboundEvent(ctx, "group:200", event)
	if err != nil || inserted || idEnriched != id {
		t.Fatalf("enriched duplicate id=%q inserted=%v err=%v", idEnriched, inserted, err)
	}
	if count := pendingInboundCount(t, store); count != 1 {
		t.Fatalf("pending count=%d", count)
	}
}

func TestInboundQueueRollsBackHistoryWhenQueueInsertFails(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "rollback.db"))
	defer func() { _ = store.Close() }()
	if _, err := store.db.Exec(`
CREATE TRIGGER reject_inbound_insert
BEFORE INSERT ON inbound_events
BEGIN
  SELECT RAISE(ABORT, 'injected queue failure');
END;
`); err != nil {
		t.Fatal(err)
	}

	event := inboundTestEvent("rollback", "must be atomic", 1)
	id, err := stableInboundEventID("group:1", event)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:1", event); err == nil {
		t.Fatal("enqueue unexpectedly succeeded")
	}
	var historyRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_events WHERE id = ?`, id).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 0 {
		t.Fatalf("message history survived rolled-back enqueue: rows=%d", historyRows)
	}
}

func TestInboundQueueFIFOLeaseRecoveryRetryAndRelease(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "leases.db"))
	defer func() { _ = store.Close() }()

	// 租约和重试窗口给足余量：CI 上几十毫秒的抖动很常见，窗口太窄会让这条用例
	// 随机失败，进而挡住构建和发版。
	const (
		leaseWindow = 300 * time.Millisecond
		retryWindow = 300 * time.Millisecond
		timingSlack = 150 * time.Millisecond
	)

	firstID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("first", "first", 200))
	if err != nil || !inserted {
		t.Fatalf("enqueue first inserted=%v err=%v", inserted, err)
	}
	time.Sleep(time.Millisecond)
	secondID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("second", "second", 100))
	if err != nil || !inserted {
		t.Fatalf("enqueue second inserted=%v err=%v", inserted, err)
	}

	first, ok, err := store.ClaimNextInboundEvent(ctx, "worker-a", time.Now().Add(time.Minute))
	if err != nil || !ok || first.ID != secondID {
		t.Fatalf("first claim item=%#v ok=%v err=%v", first, ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, first.ID, "worker-a", "ignored"); err != nil {
		t.Fatal(err)
	}
	second, ok, err := store.ClaimNextInboundEvent(ctx, "worker-a", time.Now().Add(leaseWindow))
	if err != nil || !ok || second.ID != firstID {
		t.Fatalf("second claim item=%#v ok=%v err=%v", second, ok, err)
	}

	time.Sleep(leaseWindow + timingSlack)
	recovered, ok, err := store.ClaimNextInboundEvent(ctx, "worker-b", time.Now().Add(time.Minute))
	if err != nil || !ok || recovered.ID != firstID || recovered.Attempts != 2 {
		t.Fatalf("recovered item=%#v ok=%v err=%v", recovered, ok, err)
	}
	retryAt := time.Now().Add(retryWindow)
	if err := store.RetryInboundEvent(ctx, recovered.ID, "worker-b", retryAt, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	// 只有在重试时间确实还没到时，这条断言才成立。CI 机器卡顿时这中间可能已经
	// 过了几十毫秒，断言就会假失败，所以先确认前提再断言。
	if time.Now().Before(retryAt) {
		if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker-c", time.Now().Add(time.Minute)); err != nil || ok {
			t.Fatalf("future retry was claimable ok=%v err=%v", ok, err)
		}
	}

	time.Sleep(time.Until(retryAt) + timingSlack)
	retried, ok, err := store.ClaimNextInboundEvent(ctx, "worker-c", time.Now().Add(time.Minute))
	if err != nil || !ok || retried.ID != firstID || retried.Attempts != 3 {
		t.Fatalf("retried item=%#v ok=%v err=%v", retried, ok, err)
	}
	if err := store.ReleaseInboundLeases(ctx, "worker-c"); err != nil {
		t.Fatal(err)
	}
	released, ok, err := store.ClaimNextInboundEvent(ctx, "worker-d", time.Now().Add(time.Minute))
	if err != nil || !ok || released.ID != firstID || released.Attempts != 4 {
		t.Fatalf("released item=%#v ok=%v err=%v", released, ok, err)
	}
}

func TestInboundQueueReleasesAllLeasesForStartupRecovery(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "release-all.db"))
	defer func() { _ = store.Close() }()

	for i, messageID := range []string{"first", "second"} {
		session := fmt.Sprintf("group:%d", i+1)
		if _, inserted, err := store.EnqueueInboundEvent(ctx, session, inboundTestEvent(messageID, messageID, 1)); err != nil || !inserted {
			t.Fatalf("enqueue %s inserted=%v err=%v", messageID, inserted, err)
		}
	}
	for _, owner := range []string{"worker-a", "worker-b"} {
		if _, ok, err := store.ClaimNextInboundEvent(ctx, owner, time.Now().Add(time.Minute)); err != nil || !ok {
			t.Fatalf("claim owner=%s ok=%v err=%v", owner, ok, err)
		}
	}
	if err := store.ReleaseInboundLeases(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"worker-c", "worker-d"} {
		if item, ok, err := store.ClaimNextInboundEvent(ctx, owner, time.Now().Add(time.Minute)); err != nil || !ok || item.Attempts != 2 {
			t.Fatalf("recovered owner=%s item=%#v ok=%v err=%v", owner, item, ok, err)
		}
	}
}

func TestInboundQueueClaimIsAtomic(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "atomic.db")
	stores := []*SQLiteStore{
		openInboundTestStore(t, dbPath),
		openInboundTestStore(t, dbPath),
	}
	defer func() {
		for _, store := range stores {
			_ = store.Close()
		}
	}()
	if _, inserted, err := stores[0].EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("only", "only", 1)); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}

	type claimResult struct {
		ok  bool
		err error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for i, owner := range []string{"worker-1", "worker-2"} {
		store := stores[i]
		owner := owner
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := store.ClaimNextInboundEvent(ctx, owner, time.Now().Add(time.Minute))
			results <- claimResult{ok: ok, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("atomic claims=%d, want 1", claimed)
	}
}

func TestInboundQueueSerializesEachSession(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "session-order.db"))
	defer func() { _ = store.Close() }()

	first := inboundTestEvent("a-1", "first in a", 1)
	second := inboundTestEvent("a-2", "second in a", 2)
	other := inboundTestEvent("b-1", "first in b", 3)
	for _, item := range []struct {
		session string
		event   assistant.MessageEvent
	}{{"group:a", first}, {"group:a", second}, {"group:b", other}} {
		if _, inserted, err := store.EnqueueInboundEvent(ctx, item.session, item.event); err != nil || !inserted {
			t.Fatalf("enqueue %s inserted=%v err=%v", item.event.MessageID, inserted, err)
		}
	}

	claimedFirst, ok, err := store.ClaimNextInboundEvent(ctx, "worker-1", time.Now().Add(time.Minute))
	if err != nil || !ok || claimedFirst.Event.MessageID != "a-1" {
		t.Fatalf("first claim=%#v ok=%v err=%v", claimedFirst, ok, err)
	}
	claimedOther, ok, err := store.ClaimNextInboundEvent(ctx, "worker-2", time.Now().Add(time.Minute))
	if err != nil || !ok || claimedOther.Event.MessageID != "b-1" {
		t.Fatalf("parallel claim=%#v ok=%v err=%v", claimedOther, ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, claimedFirst.ID, "worker-1", "done"); err != nil {
		t.Fatal(err)
	}
	claimedSecond, ok, err := store.ClaimNextInboundEvent(ctx, "worker-3", time.Now().Add(time.Minute))
	if err != nil || !ok || claimedSecond.Event.MessageID != "a-2" {
		t.Fatalf("second session claim=%#v ok=%v err=%v", claimedSecond, ok, err)
	}
}

func TestInboundQueuePrioritizesTriggeredEvents(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "priority.db"))
	defer func() { _ = store.Close() }()

	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("normal", "ordinary chat", 1), assistant.InboundPriorityNormal); err != nil || !inserted {
		t.Fatalf("enqueue normal inserted=%v err=%v", inserted, err)
	}
	priorityID, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("triggered", "direct trigger", 2), assistant.InboundPriorityTriggered)
	if err != nil || !inserted {
		t.Fatalf("enqueue triggered inserted=%v err=%v", inserted, err)
	}

	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 1})
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if item.ID != priorityID || item.Event.MessageID != "triggered" || item.Priority != assistant.InboundPriorityTriggered {
		t.Fatalf("claimed item=%#v", item)
	}
}

func TestInboundQueueAllowsThreeGroupTasksButKeepsPrivateSerial(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "bounded-concurrency.db"))
	defer func() { _ = store.Close() }()

	for index := 1; index <= 4; index++ {
		messageID := fmt.Sprintf("group-%d", index)
		if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent(messageID, messageID, int64(index))); err != nil || !inserted {
			t.Fatalf("enqueue %s inserted=%v err=%v", messageID, inserted, err)
		}
	}
	claimed := make([]assistant.InboundQueueItem, 0, 3)
	for index := 1; index <= 3; index++ {
		owner := fmt.Sprintf("group-worker-%d", index)
		item, ok, err := store.ClaimNextInboundEvent(ctx, owner, time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 1})
		if err != nil || !ok {
			t.Fatalf("group claim %d item=%#v ok=%v err=%v", index, item, ok, err)
		}
		claimed = append(claimed, item)
	}
	if item, ok, err := store.ClaimNextInboundEvent(ctx, "group-worker-4", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 1}); err != nil || ok {
		t.Fatalf("fourth group task escaped limit: item=%#v ok=%v err=%v", item, ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, claimed[0].ID, "group-worker-1", "done"); err != nil {
		t.Fatal(err)
	}
	if item, ok, err := store.ClaimNextInboundEvent(ctx, "group-worker-4", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 1}); err != nil || !ok || item.Event.MessageID != "group-4" {
		t.Fatalf("group task after release: item=%#v ok=%v err=%v", item, ok, err)
	}

	for index := 1; index <= 2; index++ {
		event := inboundTestEvent(fmt.Sprintf("private-%d", index), "private", int64(10+index))
		event.Kind = assistant.EventKindPrivate
		event.GroupID = ""
		if _, inserted, err := store.EnqueueInboundEvent(ctx, "private:2", event); err != nil || !inserted {
			t.Fatalf("enqueue private %d inserted=%v err=%v", index, inserted, err)
		}
	}
	private, ok, err := store.ClaimNextInboundEvent(ctx, "private-worker-1", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 1})
	if err != nil || !ok || private.Event.MessageID != "private-1" {
		t.Fatalf("private first claim=%#v ok=%v err=%v", private, ok, err)
	}
	if item, ok, err := store.ClaimNextInboundEvent(ctx, "private-worker-2", time.Now().Add(time.Minute), assistant.InboundConcurrency{Group: 3, Private: 1}); err != nil || ok {
		t.Fatalf("private session was not serial: item=%#v ok=%v err=%v", item, ok, err)
	}
}

func TestInboundQueueHistorySessionsAndWatermark(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "sessions.db"))
	defer func() { _ = store.Close() }()

	for _, event := range []assistant.MessageEvent{
		{Kind: assistant.EventKindGroup, GroupID: "100", UserID: "1", MessageID: "g1", Time: 10},
		{Kind: assistant.EventKindGroup, GroupID: "100", UserID: "2", MessageID: "g2", Time: 30},
		{Kind: assistant.EventKindGroup, GroupID: "200", UserID: "3", MessageID: "g3", Time: 20},
		{Kind: assistant.EventKindPrivate, UserID: "300", MessageID: "p1", Time: 40},
		{Kind: assistant.EventKindNotice, GroupID: "100", MessageID: "n1", Time: 50},
	} {
		session := "private:" + event.UserID
		if event.Kind == assistant.EventKindGroup || event.Kind == assistant.EventKindNotice {
			session = "group:" + event.GroupID
		}
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}

	watermark, ok, err := store.GroupHistoryWatermark(ctx, "100")
	if err != nil || !ok || watermark != 30 {
		t.Fatalf("watermark=%d ok=%v err=%v", watermark, ok, err)
	}
	if _, ok, err := store.GroupHistoryWatermark(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing watermark ok=%v err=%v", ok, err)
	}

	sessions, err := store.ListHistorySessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []assistant.HistorySession{
		{Kind: assistant.EventKindGroup, ID: "100", LastEventTime: 30},
		{Kind: assistant.EventKindGroup, ID: "200", LastEventTime: 20},
		{Kind: assistant.EventKindPrivate, ID: "300", LastEventTime: 40},
	}
	if len(sessions) != len(want) {
		t.Fatalf("sessions=%#v", sessions)
	}
	for i := range want {
		if !reflect.DeepEqual(sessions[i], want[i]) {
			t.Fatalf("sessions[%d]=%#v want %#v", i, sessions[i], want[i])
		}
	}
}

func TestSQLiteStoreConfiguresQueuePragmas(t *testing.T) {
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "pragmas.db"))
	defer func() { _ = store.Close() }()

	var journalMode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	var busyTimeout, foreignKeys int
	if err := store.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" || busyTimeout != 5000 || foreignKeys != 1 {
		t.Fatalf("journal_mode=%q busy_timeout=%d foreign_keys=%d", journalMode, busyTimeout, foreignKeys)
	}
	if max := store.db.Stats().MaxOpenConnections; max != 1 {
		t.Fatalf("MaxOpenConnections=%d", max)
	}
}

func openInboundTestStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func inboundTestEvent(messageID string, text string, eventTime int64) assistant.MessageEvent {
	return assistant.MessageEvent{
		Kind:       assistant.EventKindGroup,
		GroupID:    "1",
		UserID:     "2",
		MessageID:  messageID,
		Time:       eventTime,
		RawMessage: text,
		Segments: []assistant.MessageSegment{{
			Type: "text",
			Data: map[string]string{"text": text},
		}},
	}
}

func telegramInboundTestEvent(messageID string, text string, eventTime int64) assistant.MessageEvent {
	event := inboundTestEvent(messageID, text, eventTime)
	event.Platform = assistant.PlatformTelegram
	event.SelfID = "telegram-bot"
	return event
}

func assertEditedInboundEvent(t *testing.T, event assistant.MessageEvent, wantText string, wantEditTime int64) {
	t.Helper()
	if !event.Edited || event.EditTime != wantEditTime {
		t.Fatalf("edited event flags=%v/%d, want true/%d: %+v", event.Edited, event.EditTime, wantEditTime, event)
	}
	if event.RawMessage != wantText || assistant.PlainText(event.Segments) != wantText {
		t.Fatalf("edited event text raw=%q plain=%q, want %q", event.RawMessage, assistant.PlainText(event.Segments), wantText)
	}
}

func pendingInboundCount(t *testing.T, store *SQLiteStore) int {
	t.Helper()
	count, err := store.PendingInboundCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// TestInboundQueuePrivateConcurrencyIsConfigurable 私聊并发以前是认领 SQL 里
// 写死的 1（ELSE 1）。现在它由配置决定，群那一侧照旧。
func TestInboundQueuePrivateConcurrencyIsConfigurable(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "private-concurrency.db"))
	defer func() { _ = store.Close() }()

	for index := 1; index <= 3; index++ {
		event := inboundTestEvent(fmt.Sprintf("private-%d", index), "private", int64(index))
		event.Kind = assistant.EventKindPrivate
		event.GroupID = ""
		if _, inserted, err := store.EnqueueInboundEvent(ctx, "private:9", event); err != nil || !inserted {
			t.Fatalf("enqueue private %d inserted=%v err=%v", index, inserted, err)
		}
	}
	limits := assistant.InboundConcurrency{Group: 3, Private: 2}
	for index := 1; index <= 2; index++ {
		owner := fmt.Sprintf("private-worker-%d", index)
		if item, ok, err := store.ClaimNextInboundEvent(ctx, owner, time.Now().Add(time.Minute), limits); err != nil || !ok {
			t.Fatalf("private claim %d item=%#v ok=%v err=%v", index, item, ok, err)
		}
	}
	if item, ok, err := store.ClaimNextInboundEvent(ctx, "private-worker-3", time.Now().Add(time.Minute), limits); err != nil || ok {
		t.Fatalf("third private task escaped the configured limit: item=%#v ok=%v err=%v", item, ok, err)
	}
}

// TestInboundConcurrencyValueFallsBackToSerial 配置缺失时按会话串行，绝不能
// 变成无上限。
func TestInboundConcurrencyValueFallsBackToSerial(t *testing.T) {
	if got := inboundConcurrencyValue(nil); got.Group != 1 || got.Private != 1 {
		t.Fatalf("inboundConcurrencyValue(nil) = %#v", got)
	}
	if got := inboundConcurrencyValue([]assistant.InboundConcurrency{{Group: -5}}); got.Group != 1 || got.Private != 1 {
		t.Fatalf("inboundConcurrencyValue(negative) = %#v", got)
	}
}

// 认领时带出入队时间；「后面还有待处理消息」只认同会话、还没处理、更晚的消息，不管是谁发的。
func TestInboundSessionHasNewerPendingAndClaimEnqueuedAt(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "inbound.db"))
	defer func() { _ = store.Close() }()

	before := time.Now()
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("old", "第一句", 100)); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "worker-1", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if item.EnqueuedAt.Before(before.Add(-time.Second)) || item.EnqueuedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("EnqueuedAt=%v, want around %v", item.EnqueuedAt, before)
	}
	check := func(want bool) {
		t.Helper()
		got, err := store.InboundSessionHasNewerPending(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("InboundSessionHasNewerPending=%v, want %v", got, want)
		}
	}
	check(false)

	otherGroup := inboundTestEvent("other-group", "别的群", 102)
	otherGroup.GroupID = "9"
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:9", otherGroup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:1", inboundTestEvent("earlier", "更早的", 99)); err != nil {
		t.Fatal(err)
	}
	check(false)

	processing := inboundTestEvent("processing", "已经在处理", 101)
	processingID, _, err := store.EnqueueInboundEvent(ctx, "group:1", processing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET status = 'processing' WHERE id = ?`, processingID); err != nil {
		t.Fatal(err)
	}
	check(false)

	other := inboundTestEvent("other-user", "别人说话", 103)
	other.UserID = "3"
	if _, _, err := store.EnqueueInboundEvent(ctx, "group:1", other); err != nil {
		t.Fatal(err)
	}
	check(true)
}

func TestGroupSeqGapCountsMissingMessagesExcludingBotReplies(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "seq-gap.db"))
	defer func() { _ = store.Close() }()

	withSeq := func(seq int64, eventTime int64, userID string) assistant.MessageEvent {
		event := inboundTestEvent(fmt.Sprintf("m-%d", seq), "hello", eventTime)
		event.UserID = userID
		event.MessageSeq = fmt.Sprint(seq)
		event.ProfileID = "profile-a"
		return event
	}
	for _, event := range []assistant.MessageEvent{
		withSeq(100, 1000, "2"),
		withSeq(101, 1010, "2"),
		// 机器人自己的回复：不带 seq，但占号。
		{Kind: assistant.EventKindGroup, GroupID: "1", UserID: "42", MessageID: "self-1", Time: 1020, RawMessage: "reply"},
		// 其他机器人配置下同一个群号的消息不能拿来比较。
		func() assistant.MessageEvent { e := withSeq(104, 1030, "2"); e.ProfileID = "profile-b"; return e }(),
	} {
		if err := store.AppendMessageEvent(ctx, "group:1", event); err != nil {
			t.Fatal(err)
		}
	}

	gap, err := store.GroupSeqGap(ctx, assistant.GroupSeqGapQuery{
		ProfileID: "profile-a", GroupID: "1", SelfID: "42", Seq: 106, EventTime: 1100, Since: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 106 和 101 之间有 102~105 四个号，扣掉机器人的一条回复还缺 3 条。
	if !gap.Known || gap.PreviousSeq != 101 || gap.PreviousTime != 1010 || gap.SelfMessages != 1 || gap.Missing != 3 {
		t.Fatalf("gap = %#v", gap)
	}

	unknown, err := store.GroupSeqGap(ctx, assistant.GroupSeqGapQuery{
		ProfileID: "profile-a", GroupID: "1", SelfID: "42", Seq: 106, EventTime: 1100, Since: 1050,
	})
	if err != nil || unknown.Known {
		t.Fatalf("gap outside window = %#v err=%v", unknown, err)
	}
}

func TestGroupHistoryAnchorsListsRecentSeqMessagesNewestFirst(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "anchors.db"))
	defer func() { _ = store.Close() }()

	withSeq := func(seq int64, eventTime int64) assistant.MessageEvent {
		event := inboundTestEvent(fmt.Sprintf("m-%d", seq), "hello", eventTime)
		event.UserID = "2"
		event.MessageSeq = fmt.Sprint(seq)
		event.ProfileID = "profile-a"
		return event
	}
	for _, event := range []assistant.MessageEvent{
		withSeq(100, 1000),
		withSeq(101, 1010),
		// 机器人自己的回复不带 seq，当不了锚点。
		{Kind: assistant.EventKindGroup, GroupID: "1", UserID: "42", MessageID: "self-1", Time: 1020, RawMessage: "reply", ProfileID: "profile-a"},
		withSeq(103, 1030),
		func() assistant.MessageEvent { e := withSeq(200, 1040); e.ProfileID = "profile-b"; return e }(),
		func() assistant.MessageEvent { e := withSeq(300, 1050); e.GroupID = "2"; return e }(),
	} {
		if err := store.AppendMessageEvent(ctx, "group:"+event.GroupID, event); err != nil {
			t.Fatal(err)
		}
	}

	anchors, err := store.GroupHistoryAnchors(ctx, "profile-a", "1", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []assistant.HistoryAnchor{{MessageID: "m-103", Seq: 103}, {MessageID: "m-101", Seq: 101}}
	if len(anchors) != len(want) || anchors[0] != want[0] || anchors[1] != want[1] {
		t.Fatalf("anchors = %#v, want %#v", anchors, want)
	}
}

// 发送结果不明、在等回推确认时会延长租约：只有持有者能延，且只往后推不缩短。
func TestExtendInboundLeaseOnlyByHolderAndNeverShortens(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "inbound.db"))
	defer func() { _ = store.Close() }()
	id, _, err := store.EnqueueInboundEvent(ctx, "group:100", inboundTestEvent("message-lease", "hello", 20))
	if err != nil {
		t.Fatal(err)
	}
	original := time.Now().Add(time.Minute)
	if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker-1", original); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	leaseUntil := func() int64 {
		var value int64
		if err := store.db.QueryRowContext(ctx, `SELECT lease_until FROM inbound_events WHERE id = ?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}

	if err := store.ExtendInboundLease(ctx, id, "worker-2", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("another worker extended the lease")
	}
	extended := time.Now().Add(5 * time.Minute)
	if err := store.ExtendInboundLease(ctx, id, "worker-1", extended); err != nil {
		t.Fatal(err)
	}
	if got := leaseUntil(); got != extended.UTC().UnixNano() {
		t.Fatalf("lease_until = %d, want %d", got, extended.UTC().UnixNano())
	}
	if err := store.ExtendInboundLease(ctx, id, "worker-1", original); err != nil {
		t.Fatal(err)
	}
	if got := leaseUntil(); got != extended.UTC().UnixNano() {
		t.Fatal("extension shortened the lease")
	}
	// 延长期间别的 worker 领不到它。
	if _, ok, err := store.ClaimNextInboundEvent(ctx, "worker-2", time.Now().Add(time.Minute)); err != nil || ok {
		t.Fatalf("extended lease was reclaimed ok=%v err=%v", ok, err)
	}
}
