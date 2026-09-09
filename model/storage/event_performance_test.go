package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

func seedEventBrowsing(tb testing.TB, count int) (*SQLiteStore, time.Time) {
	tb.Helper()
	s, err := NewSQLiteStore(filepath.Join(tb.TempDir(), "events.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	now := time.Now().Truncate(time.Second)
	_, err = s.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < ?)
INSERT INTO inbound_events(id,session,kind,group_id,user_id,message_id,event_time,payload,status,available_at,created_at,updated_at,profile_id,outcome)
SELECT printf('m%d',x),'g','group','g','u',printf('m%d',x),?-x,'{}','done',?,?,?,'bot','replied' FROM seq`, count, now.Unix(), now.UnixNano(), now.UnixNano(), now.UnixNano())
	if err != nil {
		tb.Fatal(err)
	}
	_, err = s.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < ?)
INSERT INTO app_logs(id,kind,level,action,message,target,metadata,created_at)
SELECT printf('usage%d',x),'operation','info','diana.llm_usage','usage',printf('m%d',1+(x-1)/6),'{"input_tokens":1000,"output_tokens":100,"cached_input_tokens":500}',? FROM seq`, count*6, now.Format(time.RFC3339Nano))
	if err != nil {
		tb.Fatal(err)
	}
	return s, now
}

func TestLightweightEventsMatchFullPage(t *testing.T) {
	s, now := seedEventBrowsing(t, 65)
	ctx := context.Background()
	if err := s.AppendLog(ctx, applog.Entry{Action: "diana.memory.retrieved", Target: "m1", Metadata: map[string]any{"profile_id": "bot", "group_id": "g", "user_id": "u", "memories": []any{map[string]any{"id": "memory", "content": "test"}}}}); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 30, 60, 65} {
		query := InboundEventQuery{Since: now.Add(-24 * time.Hour), Limit: 30, Offset: offset, ProfileID: "bot"}
		full, err := s.ListInboundEventDetails(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		query.Lightweight = true
		light, err := s.ListInboundEventDetails(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(full.Events) != len(light.Events) || light.HasMore != (offset+30 < 65) || light.Total != 0 {
			t.Fatalf("offset %d: bad page %+v", offset, light)
		}
		for i, event := range light.Events {
			if event.ID != full.Events[i].ID || event.TotalTokens != 6600 || len(event.Memories) != 0 {
				t.Fatalf("event mismatch: %+v", event)
			}
		}
		query.Lightweight = false
		query.SummaryOnly = true
		summary, err := s.ListInboundEventDetails(ctx, query)
		if err != nil || summary.Total != full.Total || summary.TotalTokens != full.TotalTokens || len(summary.Events) != 0 {
			t.Fatalf("summary mismatch: %+v %v", summary, err)
		}
	}
	extra, err := s.LoadEventMemories(ctx, "m1")
	if err != nil || len(extra.Memories) != 1 {
		t.Fatalf("lazy memories: %+v %v", extra, err)
	}
}

func TestEventReaderIsolation(t *testing.T) {
	s, now := seedEventBrowsing(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Holding the only write connection must not stall record browsing.
	writer, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ListInboundEventDetails(ctx, InboundEventQuery{Lightweight: true, Since: now.Add(-time.Hour), Limit: 1})
	_ = writer.Close()
	if err != nil {
		t.Fatalf("reader queued behind writer: %v", err)
	}
	tx, err := s.eventReader().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM inbound_events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.EnqueueInboundEvent(ctx, "new", assistant.MessageEvent{Kind: assistant.EventKindPrivate, MessageID: "new", UserID: "u", Time: now.Unix()})
	if err != nil {
		t.Fatalf("read snapshot blocked ingest: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM inbound_events"); err == nil {
		t.Fatal("reader permits writes")
	}
}

func BenchmarkEventBrowsing(b *testing.B) {
	s, now := seedEventBrowsing(b, 6000)
	for _, light := range []bool{false, true} {
		b.Run(fmt.Sprintf("lightweight=%t", light), func(b *testing.B) {
			query := InboundEventQuery{Lightweight: light, Since: now.Add(-24 * time.Hour), Limit: 30, ProfileID: "bot"}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := s.ListInboundEventDetails(context.Background(), query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
