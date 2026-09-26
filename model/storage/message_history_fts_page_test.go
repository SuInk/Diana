// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// legacyFTSSearch 是改成单次求值之前的查询：先 COUNT，再整体排序取页。留在测试
// 里当参照，保证新查询的结果、顺序和总数一条不差。
func legacyFTSSearch(t testing.TB, s *SQLiteStore, where string, args []any, terms []string, limit int, order string, offset int) ([]string, int) {
	t.Helper()
	ctx := context.Background()
	match := messageHistoryFTSQuery(terms)
	scopedWhere := prefixMessageHistoryColumns(where)
	hits := `WITH hits AS (SELECT rowid AS rid, bm25(` + messageHistoryFTSTable + `) AS score
FROM ` + messageHistoryFTSTable + ` WHERE ` + messageHistoryFTSTable + ` MATCH ?)`
	from := `FROM message_events AS e JOIN hits AS h ON h.rid = e.rowid`
	var total int
	if err := s.eventReader().QueryRowContext(ctx, hits+` SELECT COUNT(*) `+from+` WHERE h.rid IS NOT NULL AND `+scopedWhere,
		append([]any{match}, args...)...).Scan(&total); err != nil {
		t.Fatal(err)
	}
	ordering := "h.score ASC, e.event_time DESC, e.created_at DESC, e.id DESC"
	if order == "oldest" || order == "newest" {
		ordering = historyChronologicalOrder(order, "e.")
	}
	rowArgs := append(append([]any{match}, args...), limit, max(0, offset))
	rows, err := s.eventReader().QueryContext(ctx, hits+` SELECT e.payload `+from+` WHERE h.rid IS NOT NULL AND `+scopedWhere+` ORDER BY `+ordering+` LIMIT ? OFFSET ?`, rowArgs...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, event.MessageID)
	}
	return ids, total
}

func ftsMessageIDs(events []assistant.MessageEvent) []string {
	ids := []string{}
	for _, event := range events {
		ids = append(ids, event.MessageID)
	}
	return ids
}

// 总数和当前页改成一条查询算出来、payload 改成分页后再回表，结果必须和原来的
// 两条查询完全一致：同一批命中、同样的顺序（相关度和时间都有大量并列）、同样
// 的总数，包括翻过末尾的空页。
func TestMessageHistoryFTSPageMatchesLegacyQuery(t *testing.T) {
	store := ftsStore(t)
	ctx := context.Background()
	words := []string{"凤爪", "好吃", "虎皮", "部署", "服务器", "数据库", "今天", "机器人", "diana", "deploy"}
	random := rand.New(rand.NewSource(7))
	for index := 0; index < 240; index++ {
		text := ""
		for count := 1 + random.Intn(4); count > 0; count-- {
			text += words[random.Intn(len(words))] + " "
		}
		group := fmt.Sprintf("g%d", index%3)
		// 时间只取几个值，逼出大量并列，检验并列时的次序也没变。
		event := historySearchEvent(int64(100+index%7), group, fmt.Sprintf("m%03d", index), "Alice", text)
		if err := store.AppendMessageEvent(ctx, "onebot-main:group:"+group, event); err != nil {
			t.Fatal(err)
		}
	}
	type scope struct {
		where string
		args  []any
	}
	base := `kind != ? AND event_time BETWEEN ? AND ?`
	scopes := map[string]scope{
		"same":    {base + ` AND session = ?`, []any{"notice", int64(0), int64(1000), "onebot-main:group:g1"}},
		"cross":   {base + ` AND session LIKE ? ESCAPE '\' AND session != ?`, []any{"notice", int64(0), int64(1000), "onebot-main:group:%", "onebot-main:group:g0"}},
		"window":  {base + ` AND session LIKE ? ESCAPE '\'`, []any{"notice", int64(102), int64(104), "onebot-main:group:%"}},
		"nothing": {base + ` AND session = ?`, []any{"notice", int64(0), int64(1000), "onebot-main:group:none"}},
	}
	largest := 0
	for name, scope := range scopes {
		for _, terms := range [][]string{{"凤爪好吃", "凤爪", "好吃"}, {"服务器"}, {"dia"}, {"今天", "机器人", "deploy"}} {
			for _, order := range []string{"", "newest", "oldest"} {
				for _, page := range []struct{ limit, offset int }{{20, 0}, {7, 5}, {50, 30}, {20, 500}} {
					label := fmt.Sprintf("%s/%v/%q/%d+%d", name, terms, order, page.offset, page.limit)
					wantIDs, wantTotal := legacyFTSSearch(t, store, scope.where, scope.args, terms, page.limit, order, page.offset)
					events, total, ok, err := store.searchMessageEventsFTS(ctx, scope.where, scope.args, terms, page.limit, order, page.offset)
					if err != nil || !ok {
						t.Fatalf("%s: ok=%v err=%v", label, ok, err)
					}
					if total != wantTotal || !reflect.DeepEqual(ftsMessageIDs(events), wantIDs) {
						t.Fatalf("%s: got total=%d %v, want total=%d %v", label, total, ftsMessageIDs(events), wantTotal, wantIDs)
					}
					largest = max(largest, total)
				}
			}
		}
	}
	if largest < 80 {
		t.Fatalf("命中太少（最多 %d 条），翻页和并列排序没有被真正覆盖", largest)
	}
}
