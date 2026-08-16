// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSQLiteStoreRecordsRealFavorabilityChangesNewestFirst(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "favorability.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{
		Kind:      assistant.EventKindGroup,
		GroupID:   "20001",
		UserID:    "10002",
		MessageID: "30001",
	}
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		FavorabilityDelta:        2,
		FavorabilityChangeSource: "interaction",
		FavorabilityChangeReason: "明确表达感谢",
		Administrative:           true,
	}); err != nil {
		t.Fatal(err)
	}
	setValue := 50
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		SetFavorability:            &setValue,
		FavorabilityChangeSource:   "owner_set",
		FavorabilityChangeReason:   "活动奖励",
		FavorabilityChangeOperator: "10001",
		Administrative:             true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		SetFavorability:          &setValue,
		FavorabilityChangeSource: "owner_set",
		Administrative:           true,
	}); err != nil {
		t.Fatal(err)
	}

	changes, err := store.ListUserFavorabilityChanges(ctx, "10002", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes=%#v, want 2 real changes", changes)
	}
	if changes[0].Delta != 48 || changes[0].Before != 2 || changes[0].After != 50 || changes[0].Source != "owner_set" || changes[0].Reason != "活动奖励" || changes[0].OperatorID != "10001" {
		t.Fatalf("newest change=%#v", changes[0])
	}
	if changes[1].Delta != 2 || changes[1].Source != "interaction" || changes[1].Reason != "明确表达感谢" || changes[1].GroupID != "20001" || changes[1].MessageID != "30001" {
		t.Fatalf("oldest change=%#v", changes[1])
	}

	limited, err := store.ListUserFavorabilityChanges(ctx, "10002", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].ID != changes[0].ID {
		t.Fatalf("limited changes=%#v", limited)
	}
}

func TestSQLiteStoreDoesNotRecordOwnerProfileInitializationAsChange(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{UserID: "10001"}, assistant.UserMemoryUpdate{OwnerID: "10001"}); err != nil {
		t.Fatal(err)
	}
	changes, err := store.ListUserFavorabilityChanges(ctx, "10001", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("owner initialization changes=%#v", changes)
	}
}
