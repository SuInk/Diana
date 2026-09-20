package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestLLMParamDowngradesRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "downgrades.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// 没存过时不该报错，调用方据此什么都不恢复。
	records, err := store.LoadLLMParamDowngrades(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%#v err=%v", records, err)
	}

	learnedAt := time.Now().Truncate(time.Second).UTC()
	want := []llm.DowngradeRecord{{Key: "openai_compatible|https://gw.invalid/v1|thinking", Field: "tool_choice", LearnedAt: learnedAt}}
	if err := store.SaveLLMParamDowngrades(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadLLMParamDowngrades(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if got[0].Key != want[0].Key || got[0].Field != want[0].Field || !got[0].LearnedAt.Equal(learnedAt) {
		t.Fatalf("round trip lost data: %#v", got[0])
	}
}
