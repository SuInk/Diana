package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestContextResetPersistsWithoutDeletingArchive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()
	session := "bot:group:one"
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "one", UserID: "owner", MessageID: "old", Time: now.Unix(), RawMessage: "旧话题"}
	appendEvent := func(session string, event assistant.MessageEvent) {
		t.Helper()
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(session, event)
	appendEvent("bot:group:two", event)
	appendEvent("other-bot:group:one", event)
	if err := store.ResetContextHistory(ctx, session, now); err != nil {
		t.Fatal(err)
	}
	// Replaying an old ID must not move it into the new generation, even in the same second.
	appendEvent(session, event)
	next := event
	next.MessageID = "new"
	next.RawMessage = "新话题"
	appendEvent(session, next)
	delayed := event
	delayed.MessageID = "backfill"
	delayed.Time = now.Add(-time.Hour).Unix()
	appendEvent(session, delayed)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ListContextMessageEvents(ctx, session, 20)
	if err != nil || len(got) != 1 || got[0].MessageID != "new" {
		t.Fatalf("context=%v err=%v", got, err)
	}
	archive, err := store.ListRecentMessageEvents(ctx, session, 20)
	if err != nil || len(archive) != 3 {
		t.Fatalf("archive=%v err=%v", archive, err)
	}
	for _, other := range []string{"bot:group:two", "other-bot:group:one"} {
		got, err = store.ListContextMessageEvents(ctx, other, 20)
		if err != nil || len(got) != 1 {
			t.Fatalf("other=%s events=%v err=%v", other, got, err)
		}
	}
	if err := store.ResetContextHistory(ctx, session, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListContextMessageEvents(ctx, session, 20)
	if err != nil || len(got) != 0 {
		t.Fatalf("second reset=%v err=%v", got, err)
	}
}

func TestContextResetClearsTemporaryStateOnly(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()
	request := threadWriteRequest("旧任务", now, "m1")
	if _, err := store.ApplyMemoryCandidates(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.Candidates[0].Kind = assistant.MemoryKindFact
	request.Candidates[0].Key = "fact.keep"
	request.Candidates[0].Content = "长期事实"
	if _, err := store.ApplyMemoryCandidates(ctx, request); err != nil {
		t.Fatal(err)
	}
	state, err := store.PutThreadState(ctx, assistant.ThreadStatePutRequest{ProfileID: "bot", Session: request.Session, UserID: "owner", Scope: assistant.ThreadStateScopeUser, TaskKind: "game", State: []byte(`{"answer":"a"}`), Now: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ResetContextHistory(ctx, request.Session, now); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM thread_states WHERE id=?`, state.ID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{Session: request.Session, Now: now, MaxCandidates: 20, CurrentSessionOnly: true})
	if err != nil || len(items) != 1 || items[0].Kind != assistant.MemoryKindFact {
		t.Fatalf("memories=%v err=%v", items, err)
	}
}

func TestContextHistoryMigratesExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "owner", MessageID: "legacy", Time: time.Now().Unix(), RawMessage: "旧版历史"}
	if err := store.AppendMessageEvent(ctx, "private:owner", event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE message_events DROP COLUMN context_generation`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE session_contexts`); err != nil {
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
	got, err := store.ListContextMessageEvents(ctx, "private:owner", 20)
	if err != nil || len(got) != 1 || got[0].MessageID != "legacy" {
		t.Fatalf("migrated context=%v err=%v", got, err)
	}
	if err := store.ResetContextHistory(ctx, "private:owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListContextMessageEvents(ctx, "private:owner", 20)
	if err != nil || len(got) != 0 {
		t.Fatalf("reset migrated context=%v err=%v", got, err)
	}
}
