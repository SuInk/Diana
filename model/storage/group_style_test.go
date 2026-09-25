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

func TestGroupStyleRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, found, err := store.GroupStyle(ctx, "bot", "g1"); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	at := time.Unix(1_790_000_000, 0)
	want := assistant.GroupStyle{ProfileID: "bot", GroupID: "g1", Text: "大家爱说「绷不住了」", Manual: true, SampleCount: 120, UpdatedAt: at}
	if err := store.SaveGroupStyle(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GroupStyle(ctx, "bot", "g1")
	if err != nil || !found || got.Text != want.Text || !got.Manual || got.SampleCount != 120 || !got.UpdatedAt.Equal(at) {
		t.Fatalf("got %#v found=%v err=%v", got, found, err)
	}
	want.Text, want.Manual = "改过了", false
	if err := store.SaveGroupStyle(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.GroupStyle(ctx, "bot", "g1"); got.Text != "改过了" || got.Manual {
		t.Fatalf("overwrite lost: %#v", got)
	}
	if _, found, _ := store.GroupStyle(ctx, "other-bot", "g1"); found {
		t.Fatal("style leaked across bots")
	}
	if err := store.DeleteGroupStyle(ctx, "bot", "g1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.GroupStyle(ctx, "bot", "g1"); found {
		t.Fatal("delete did not remove the style")
	}
}
