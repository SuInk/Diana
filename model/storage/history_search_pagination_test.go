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

func TestHistorySearchChronologicalRanksAllWordMatchesFirst(t *testing.T) {
	ctx := context.Background()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tiers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 目标消息最早，后面一串只提到 deepseek 或只提到 harness 的消息更新。
	events := []assistant.MessageEvent{historySearchEvent(10, "one", "target", "bot", "Deepseek Harness 发布了官方客户端 下载地址")}
	for i := 0; i < 6; i++ {
		events = append(events, historySearchEvent(int64(20+i), "one", fmt.Sprint("deepseek-", i), "sender", "deepseek 今天又降价了"))
		events = append(events, historySearchEvent(int64(40+i), "one", fmt.Sprint("harness-", i), "sender", "这个 harness 怎么设计"))
	}
	for _, event := range events {
		if err := s.AppendMessageEvent(ctx, "bot:group:one", event); err != nil {
			t.Fatal(err)
		}
	}
	ftsAvailable := s.historyFTS
	for _, useFTS := range []bool{false, true} {
		if useFTS && !ftsAvailable {
			continue
		}
		s.historyFTS = useFTS
		for _, order := range []string{"newest", "oldest"} {
			q := assistant.MessageHistorySearchQuery{Session: "bot:group:one", Text: "DeepSeek harness", ThroughTime: 100, Limit: 5, Sort: order}
			var ids []string
			for q.Offset < len(events) {
				rows, total, err := s.SearchMessageEvents(ctx, q)
				if err != nil || total != len(events) || len(rows) == 0 {
					t.Fatalf("fts=%v order=%s rows=%d total=%d err=%v", useFTS, order, len(rows), total, err)
				}
				for _, row := range rows {
					ids = append(ids, row.MessageID)
				}
				q.Offset += len(rows)
			}
			if len(ids) != len(events) || ids[0] != "target" {
				t.Fatalf("fts=%v order=%s ids=%v, want target first and every match once", useFTS, order, ids)
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if seen[id] {
					t.Fatalf("fts=%v order=%s duplicate %s in %v", useFTS, order, id, ids)
				}
				seen[id] = true
			}
		}
		// 单个词不分档，仍是纯时间顺序。
		rows, _, err := s.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{Session: "bot:group:one", Text: "deepseek", ThroughTime: 100, Limit: 1, Sort: "newest"})
		if err != nil || len(rows) != 1 || rows[0].MessageID != "deepseek-5" {
			t.Fatalf("fts=%v single word newest=%v err=%v", useFTS, rows, err)
		}
	}
}
