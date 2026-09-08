package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestImageModelRecordsSurviveRestartAndStayScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, scope := range []string{"bot-a/group", "bot-b/group"} {
		err = store.SaveImageModelRecord(ctx, scope, assistant.ImageModelRecord{MessageID: "42", CreatedAt: 10, Models: []assistant.GeneratedImageModel{{ModelID: scope, Operation: "generate"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, scope := range []string{"bot-a/group", "bot-b/group"} {
		for _, id := range []string{"42", ""} {
			record, found, err := store.LoadImageModelRecord(ctx, scope, id)
			if err != nil || !found || record.Models[0].ModelID != scope {
				t.Fatalf("record=%+v found=%v err=%v", record, found, err)
			}
		}
	}
	if _, found, err := store.LoadImageModelRecord(ctx, "other", "42"); err != nil || found {
		t.Fatalf("cross-scope lookup: found=%v err=%v", found, err)
	}
	if _, found, err := store.LoadImageModelRecord(ctx, "bot-a/group", "old"); err != nil || found {
		t.Fatalf("invented old provenance: found=%v err=%v", found, err)
	}
}
