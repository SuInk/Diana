package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestInboundRetrySurvivesRestartAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot", GroupID: "g", UserID: "u", MessageID: "m", Time: time.Now().Unix(), RawMessage: "keep me"}
	writer, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInboundRetry(ctx, "group:g", event, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInboundRetry(ctx, "group:g", event, 100); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	if _, err := s.ReplayInboundRetries(blocked, 32); err == nil {
		t.Fatal("replay unexpectedly acquired held writer")
	}
	cancel()
	entries, _ := os.ReadDir(s.retryDirectory())
	if len(entries) != 1 {
		t.Fatal("failed retry lost journal")
	}
	info, _ := entries[0].Info()
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	_ = writer.Close()
	_ = s.Close()
	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if count, err := s.ReplayInboundRetries(ctx, 32); err != nil || count != 1 {
		t.Fatalf("restart replay: %d %v", count, err)
	}
	if err := s.SaveInboundRetry(ctx, "group:g", event, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplayInboundRetries(ctx, 32); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM inbound_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate delivery: %d %v", count, err)
	}
	entries, _ = os.ReadDir(s.retryDirectory())
	if len(entries) != 0 {
		t.Fatal("committed journal not removed")
	}
}

func TestInboundRetryJournalFailureIsReported(t *testing.T) {
	s, _ := seedEventBrowsing(t, 1)
	if err := os.WriteFile(s.retryDirectory(), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInboundRetry(context.Background(), "g", assistant.MessageEvent{Kind: assistant.EventKindGroup, MessageID: "m", GroupID: "g", UserID: "u"}, 0); err == nil {
		t.Fatal("failed journal accepted message")
	}
}

func TestInboundRetryQuarantinesCorruptionWithoutBlocking(t *testing.T) {
	s, now := seedEventBrowsing(t, 1)
	if err := os.MkdirAll(s.retryDirectory(), 0700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(s.retryDirectory(), "000-corrupt.json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "u", MessageID: "valid", Time: now.Unix()}
	if err := s.SaveInboundRetry(context.Background(), "private:u", event, 0); err != nil {
		t.Fatal(err)
	}
	count, err := s.ReplayInboundRetries(context.Background(), 1)
	if count != 1 || err == nil {
		t.Fatalf("valid entry blocked or corruption hidden: %d %v", count, err)
	}
	if _, err := os.Stat(bad + ".invalid"); err != nil {
		t.Fatal("corrupt entry not preserved")
	}
}
