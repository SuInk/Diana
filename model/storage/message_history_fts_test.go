// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func ftsStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history-fts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if !store.historyFTS {
		t.Fatal("FTS5 索引没有建立起来，检索会静默退回全表扫描")
	}
	return store
}

// 索引必须真的被建起来并回填，否则所有改动等于没做——检索会静默走回 LIKE。
func TestMessageHistoryFTSIndexIsBuiltAndBackfilled(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "backfill.db")

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessageEvent(ctx, "onebot-main:group:one",
		historySearchEvent(10, "one", "before", "Alice", "转发合并功能坏掉了")); err != nil {
		t.Fatal(err)
	}
	// 手动清空索引，模拟「库里已有存量数据、索引还没建」的升级场景。
	if _, err := store.db.Exec(`DELETE FROM ` + messageHistoryFTSTable); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	var indexed int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM ` + messageHistoryFTSTable).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("重开库时应当回填存量数据，索引里有 %d 行", indexed)
	}
}

// 索引要跟住写入和更新，否则会和正表漂移。检索 token 在 Go 侧切分，
// 触发器做不到，所以同步逻辑在 AppendMessageEvent 里。
func TestMessageHistoryFTSTracksWrites(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	session := "onebot-main:group:one"

	event := historySearchEvent(10, "one", "m1", "Alice", "转发合并功能坏掉了")
	if err := store.AppendMessageEvent(ctx, session, event); err != nil {
		t.Fatal(err)
	}
	countIndexed := func() int {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+messageHistoryFTSTable+` WHERE `+messageHistoryFTSTable+` MATCH ?`,
			messageHistoryFTSQuery([]string{"转发合并"})).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if countIndexed() != 1 {
		t.Fatal("插入后索引里没有这条")
	}

	// 同一条消息重新写入会走 ON CONFLICT DO UPDATE，索引必须跟着换内容。
	updated := historySearchEvent(10, "one", "m1", "Alice", "已经改成别的内容了")
	if err := store.AppendMessageEvent(ctx, session, updated); err != nil {
		t.Fatal(err)
	}
	if countIndexed() != 0 {
		t.Fatal("更新后索引仍然命中旧内容")
	}
	var total int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + messageHistoryFTSTable).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("更新不该让索引多出一行，实际 %d 行", total)
	}
}

// 长词走索引、短词走 LIKE，两边的结果要取并集——中文 2 字词太常见，丢了召回会明显变差。
func TestMessageHistorySearchKeepsShortTermRecall(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	session := "onebot-main:group:one"
	for _, event := range []assistant.MessageEvent{
		historySearchEvent(30, "one", "partial", "Alice", "这家的凤爪味道不错"),
		historySearchEvent(20, "one", "exact", "Bob", "虎皮凤爪很好吃"),
		historySearchEvent(10, "one", "noise", "Carol", "今天讨论别的菜"),
	} {
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	events, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		Session: session, Text: "虎皮凤爪好吃吗", Terms: []string{"虎皮凤爪", "凤爪", "好吃"},
		FromTime: 0, ThroughTime: 100, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(events) != 2 {
		t.Fatalf("短词召回丢失：total=%d len=%d", total, len(events))
	}
	// 命中索引的排前面，只靠 2 字词命中的排后面。
	if events[0].MessageID != "exact" || events[1].MessageID != "partial" {
		t.Fatalf("排序不对：%s, %s", events[0].MessageID, events[1].MessageID)
	}
}

// 跨会话检索必须仍然被 session 前缀限死，不能因为换了索引就漏到别的命名空间。
func TestMessageHistoryFTSStillScopesCrossSessionSearch(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	if err := store.AppendMessageEvent(ctx, "onebot-main:group:one",
		historySearchEvent(10, "one", "mine", "Alice", "转发合并功能坏掉了")); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessageEvent(ctx, "telegram-other:group:two",
		historySearchEvent(20, "two", "theirs", "Bob", "转发合并功能也坏掉了")); err != nil {
		t.Fatal(err)
	}
	events, _, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		CrossSession: true, SessionPrefix: "onebot-main:group:", Text: "转发合并",
		Terms: []string{"转发合并"}, FromTime: 0, ThroughTime: 100, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.MessageID == "theirs" {
			t.Fatalf("跨会话检索漏到了别的命名空间：%#v", events)
		}
	}
	if len(events) != 1 {
		t.Fatalf("应当只命中本命名空间的一条，实际 %d 条", len(events))
	}
}

// 时间窗仍然要生效。
func TestMessageHistoryFTSRespectsTimeWindow(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	session := "onebot-main:group:one"
	if err := store.AppendMessageEvent(ctx, session,
		historySearchEvent(10, "one", "old", "Alice", "转发合并功能坏掉了")); err != nil {
		t.Fatal(err)
	}
	events, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		Session: session, Text: "转发合并", Terms: []string{"转发合并"},
		FromTime: 50, ThroughTime: 100, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(events) != 0 {
		t.Fatalf("时间窗外的记录不该被检索到：total=%d events=%d", total, len(events))
	}
}
