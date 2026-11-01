// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestNotebookCreateOnlyProtectsExistingEntries(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		store := newNotebookStore(t)
		ctx := context.Background()
		original := upsertNotebook(t, store, assistant.NotebookUpsertRequest{ScopeKey: "bot:tg", Term: "Ubuntu", Aliases: []string{"uuu"}, Meaning: "人工释义", EditorUserID: "human"})
		if deleted {
			var err error
			original, _, err = store.DeleteNotebookEntry(ctx, "bot:tg", "Ubuntu", "human", "Human", "作废", time.Now())
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, term := range []string{"Ubuntu", "UUU"} {
			entry, created, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{ScopeKey: "tg:group:1", Term: term, Meaning: "自动释义", CreateOnly: true, GuardScopes: []string{"bot:tg"}})
			if err != nil || created || entry.ID != original.ID || entry.Version != original.Version || entry.Meaning != original.Meaning || entry.Status != original.Status {
				t.Fatalf("entry=%+v created=%v err=%v", entry, created, err)
			}
		}
		entries, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{ScopeKeys: []string{"tg:group:1"}, IncludeDeleted: true})
		if err != nil || len(entries) != 0 {
			t.Fatalf("unexpected shadow entry: %+v %v", entries, err)
		}
	}
}

func TestNotebookCreateOnlyConcurrentReplay(t *testing.T) {
	store := newNotebookStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{ScopeKey: "tg:group:1", Term: "uuu", Meaning: "Ubuntu", CreateOnly: true})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entry, found, err := store.NotebookEntryDetail(ctx, "tg:group:1", "uuu")
	if err != nil || !found || entry.Version != 1 || len(entry.Revisions) != 1 {
		t.Fatalf("entry=%+v found=%v err=%v", entry, found, err)
	}
}
