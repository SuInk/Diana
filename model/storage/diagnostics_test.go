package storage

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

func TestStorageDiagnosticsIdentifyConnectionWait(t *testing.T) {
	s, _ := seedEventBrowsing(t, 1)
	writer, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.beginWriteTx(ctx, "test_enqueue"); err == nil {
		t.Fatal("write unexpectedly acquired connection")
	}
	for _, field := range []string{"operation=test_enqueue.begin_transaction", "pool=write", "pool_wait_count_delta=1", "context_error=context deadline exceeded"} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("missing %s: %s", field, output.String())
		}
	}
}
