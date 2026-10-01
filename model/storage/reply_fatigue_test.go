package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestReplyFatigueRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "fatigue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// 没存过时不该报错，调用方据此什么都不恢复。
	records, err := store.LoadReplyFatigue(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%#v err=%v", records, err)
	}

	at := time.Now().Truncate(time.Second).UTC()
	want := []assistant.ReplyFatigueRecord{{Key: "group:10001\x0020002", Fast: 0.8, Slow: 1.5, Engage: 0.6, At: at}}
	if err := store.SaveReplyFatigue(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadReplyFatigue(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if got[0].Key != want[0].Key || got[0].Fast != 0.8 || got[0].Slow != 1.5 || got[0].Engage != 0.6 || !got[0].At.Equal(at) {
		t.Fatalf("round trip lost data: %#v", got[0])
	}
}
