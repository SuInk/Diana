package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
)

func TestGroupPromptSessionRestartIsolationAndStaleResetWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state := assistant.GroupPromptSession{Anchor: "first", LoadedTools: []string{"read_file", "render"}, History: []assistant.GroupPromptHistoryEntry{{Key: "first", SourceHash: "hash", Messages: []llm.Message{{Role: llm.RoleUser, Content: "public history"}}}}}
	for _, scope := range []string{"bot/one", "other/one"} {
		if ok, err := store.SaveGroupPromptSession(ctx, scope, "group:one", state); err != nil || !ok {
			t.Fatalf("save %v %v", ok, err)
		}
	}
	if ok, err := store.SaveGroupPromptSession(ctx, "bot/two", "group:two", state); err != nil || !ok {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.LoadGroupPromptSession(ctx, "bot/one", "group:one")
	if err != nil || got.Anchor != "first" || len(got.LoadedTools) != 2 || got.History[0].Messages[0].Content != "public history" {
		t.Fatalf("restart %+v %v", got, err)
	}
	missing, err := store.LoadGroupPromptSession(ctx, "missing", "group:one")
	if err != nil || len(missing.History) != 0 {
		t.Fatal("scope leak", err)
	}
	if err := store.ResetContextHistory(ctx, "group:one", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.SaveGroupPromptSession(ctx, "bot/one", "group:one", state); err != nil || ok {
		t.Fatalf("stale writer accepted %v %v", ok, err)
	}
	for _, scope := range []string{"bot/one", "other/one"} {
		got, err = store.LoadGroupPromptSession(ctx, scope, "group:one")
		if err != nil || got.Generation != 1 || len(got.History) != 0 || len(got.LoadedTools) != 0 {
			t.Fatalf("reset %+v %v", got, err)
		}
	}
	got, err = store.LoadGroupPromptSession(ctx, "bot/two", "group:two")
	if err != nil || len(got.LoadedTools) != 2 {
		t.Fatal("other group lost", err)
	}
}
