package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestHistorySearchChronologicalPagesKeepTimestampTiesAndScope(t *testing.T) {
	ctx := context.Background()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, at := range []int64{10, 20, 20, 20, 30} {
		e := historySearchEvent(at, "one", fmt.Sprint(i), "sender", "快递待取件")
		if err := s.AppendMessageEvent(ctx, "bot:group:one", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendMessageEvent(ctx, "other:group:one", historySearchEvent(1, "one", "foreign", "sender", "快递")); err != nil {
		t.Fatal(err)
	}
	ftsAvailable := s.historyFTS
	for _, useFTS := range []bool{false, true} {
		if useFTS && !ftsAvailable {
			continue
		}
		s.historyFTS = useFTS
		for _, order := range []string{"oldest", "newest"} {
			q := assistant.MessageHistorySearchQuery{Session: "bot:group:one", Text: "快递", ThroughTime: 100, Limit: 2, Sort: order}
			seen := map[string]bool{}
			var times []int64
			for q.Offset < 5 {
				rows, total, err := s.SearchMessageEvents(ctx, q)
				if err != nil || total != 5 || len(rows) == 0 {
					t.Fatalf("fts=%v order=%s rows=%v total=%d err=%v", useFTS, order, rows, total, err)
				}
				for _, row := range rows {
					if seen[row.MessageID] || row.MessageID == "foreign" {
						t.Fatal("duplicate or foreign match")
					}
					seen[row.MessageID] = true
					times = append(times, row.Time)
				}
				q.Offset += len(rows)
			}
			for i := 1; i < len(times); i++ {
				if order == "oldest" && times[i] < times[i-1] || order == "newest" && times[i] > times[i-1] {
					t.Fatalf("wrong order: %v", times)
				}
			}
		}
	}
}
