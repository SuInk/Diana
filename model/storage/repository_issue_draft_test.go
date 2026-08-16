package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestSQLiteStorePersistsRepositoryIssueDrafts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "drafts.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	draft := assistant.RepositoryIssueDraft{
		ID:            "draft-1",
		Platform:      "onebot-v11",
		GroupID:       "group-1",
		Repository:    "acme/demo",
		RequesterID:   "member-1",
		RequesterName: "Alice",
		Input:         map[string]any{"title": "Login failed", "body": "Cannot sign in."},
		Status:        "pending",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.SaveRepositoryIssueDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	loaded, ok, err := store.RepositoryIssueDraft(ctx, draft.ID)
	if err != nil || !ok {
		t.Fatalf("RepositoryIssueDraft() ok=%v err=%v", ok, err)
	}
	if loaded.GroupID != draft.GroupID || loaded.Repository != draft.Repository || loaded.Status != "pending" || loaded.RequesterName != "Alice" {
		t.Fatalf("loaded draft=%#v", loaded)
	}

	loaded.Status = "created"
	loaded.IssueNumber = 42
	loaded.IssueURL = "https://github.com/acme/demo/issues/42"
	loaded.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveRepositoryIssueDraft(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListRepositoryIssueDrafts(ctx, "group-1", "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending drafts=%#v err=%v", pending, err)
	}
	created, err := store.ListRepositoryIssueDrafts(ctx, "group-1", "created")
	if err != nil || len(created) != 1 || created[0].IssueNumber != 42 {
		t.Fatalf("created drafts=%#v err=%v", created, err)
	}
}
