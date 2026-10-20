// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestMoodStatesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	empty, err := store.LoadMoodStates(ctx)
	if err != nil || len(empty) != 0 {
		t.Fatalf("fresh store moods = %v, %v", empty, err)
	}
	want := map[string]assistant.MoodSnapshot{"bot": {Score: 2.5, UpdatedAt: 1700000000123456789}}
	if err := store.SaveMoodStates(ctx, want); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.LoadMoodStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["bot"] != want["bot"] {
		t.Fatalf("mood after reopen = %+v, want %+v", got["bot"], want["bot"])
	}
}
