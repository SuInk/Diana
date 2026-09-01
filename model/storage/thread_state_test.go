// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestThreadStatePersistsUpdatesAndTerminates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thread-state.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_788_247_000, 0)
	created, err := store.PutThreadState(context.Background(), assistant.ThreadStatePutRequest{
		ProfileID: "bot-1", Session: "group:1", UserID: "user-a", TaskKind: "guess.character",
		State: json.RawMessage(`{"target":"DIO"}`), SourceMessageID: "m1", Now: now, ExpiresAt: now.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.Status != assistant.ThreadStateActive {
		t.Fatalf("created = %#v", created)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	items, err := store.ListActiveThreadStates(context.Background(), "bot-1", "group:1", "user-a", now.Add(time.Minute), 4)
	if err != nil || len(items) != 1 || string(items[0].State) != `{"target":"DIO"}` {
		t.Fatalf("reloaded = %#v, err=%v", items, err)
	}

	updated, err := store.PutThreadState(context.Background(), assistant.ThreadStatePutRequest{
		ProfileID: "bot-1", Session: "group:1", UserID: "user-a", TaskKind: "guess.character",
		State: json.RawMessage(`{"target":"DIO","turn":2}`), ExpectedVersion: 1,
		Now: now.Add(2 * time.Minute), ExpiresAt: now.Add(32 * time.Minute),
	})
	if err != nil || updated.ID != created.ID || updated.Version != 2 {
		t.Fatalf("updated = %#v, err=%v", updated, err)
	}
	_, err = store.PutThreadState(context.Background(), assistant.ThreadStatePutRequest{
		ProfileID: "bot-1", Session: "group:1", UserID: "user-a", TaskKind: "guess.character",
		State: json.RawMessage(`{"target":"other"}`), ExpectedVersion: 1,
		Now: now.Add(3 * time.Minute), ExpiresAt: now.Add(33 * time.Minute),
	})
	if !errors.Is(err, assistant.ErrThreadStateVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}

	ended, err := store.EndThreadState(context.Background(), assistant.ThreadStateEndRequest{
		ProfileID: "bot-1", Session: "group:1", UserID: "user-a", TaskKind: "guess.character",
		ExpectedVersion: 2, Status: assistant.ThreadStateCompleted, Now: now.Add(4 * time.Minute),
	})
	if err != nil || ended.Status != assistant.ThreadStateCompleted || len(ended.State) != 0 {
		t.Fatalf("ended = %#v, err=%v", ended, err)
	}
	items, err = store.ListActiveThreadStates(context.Background(), "bot-1", "group:1", "user-a", now.Add(5*time.Minute), 4)
	if err != nil || len(items) != 0 {
		t.Fatalf("active after complete = %#v, err=%v", items, err)
	}
	var memories int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_items`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories != 0 {
		t.Fatalf("temporary state leaked into long-term memory: %d rows", memories)
	}
}

func TestThreadStateIsolationAndExpiry(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "thread-state-isolation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Unix(1_788_247_000, 0)
	for _, userID := range []string{"user-a", "user-b"} {
		_, err := store.PutThreadState(context.Background(), assistant.ThreadStatePutRequest{
			ProfileID: "bot-1", Session: "group:1", UserID: userID, TaskKind: "guess.character",
			State: json.RawMessage(`{"target":"` + userID + `"}`), Now: now, ExpiresAt: now.Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListActiveThreadStates(context.Background(), "bot-1", "group:1", "user-a", now.Add(30*time.Second), 4)
	if err != nil || len(items) != 1 || string(items[0].State) != `{"target":"user-a"}` {
		t.Fatalf("isolated = %#v, err=%v", items, err)
	}
	items, err = store.ListActiveThreadStates(context.Background(), "bot-1", "group:1", "user-a", now.Add(2*time.Minute), 4)
	if err != nil || len(items) != 0 {
		t.Fatalf("expired = %#v, err=%v", items, err)
	}
	var status, stateJSON string
	if err := store.db.QueryRow(`SELECT status, state_json FROM thread_states WHERE user_id='user-a'`).Scan(&status, &stateJSON); err != nil {
		t.Fatal(err)
	}
	if status != string(assistant.ThreadStateExpired) || stateJSON != "{}" {
		t.Fatalf("expired row status=%q state=%q", status, stateJSON)
	}
}
