package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestSemanticReferenceCachePersistsAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "semantic-cache.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := assistant.SemanticReferenceCacheRecord{CacheKey: "key", MessageIDs: []string{"image-1", "voice-2"}, Confidence: 0.92, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix()}
	if err := store.SaveSemanticReferenceCache(ctx, want); err != nil {
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
	got, found, err := store.LoadSemanticReferenceCache(ctx, want.CacheKey)
	if err != nil || !found || got.Confidence != want.Confidence || len(got.MessageIDs) != 2 || got.MessageIDs[1] != "voice-2" {
		t.Fatalf("cache found=%v got=%#v err=%v", found, got, err)
	}
}
