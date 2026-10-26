// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

func TestMessageHistoryFTSTracksTelegramEdit(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	session := "telegram-main:group:one"

	original := telegramInboundTestEvent("telegram-edit-fts", "cobaltlegacytoken", 40)
	if _, inserted, err := store.EnqueueInboundEvent(ctx, session, original); err != nil || !inserted {
		t.Fatalf("enqueue original inserted=%v err=%v", inserted, err)
	}
	edited := telegramInboundTestEvent(original.MessageID, "magentaeditedtoken", original.Time)
	edited.Edited = true
	edited.EditTime = 45
	if refresh, err := store.RefreshInboundMessageEdit(ctx, session, edited); err != nil || !refresh.Found || !refresh.Applied {
		t.Fatalf("refresh edit=%+v err=%v", refresh, err)
	}

	search := func(term string) ([]assistant.MessageEvent, int) {
		t.Helper()
		events, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
			Session: session, Text: term, Terms: []string{term}, FromTime: 0, ThroughTime: 100, Limit: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		return events, total
	}
	if events, total := search("cobaltlegacytoken"); total != 0 || len(events) != 0 {
		t.Fatalf("old edit text remains searchable: total=%d events=%#v", total, events)
	}
	if events, total := search("magentaeditedtoken"); total != 1 || len(events) != 1 || events[0].MessageID != original.MessageID || !events[0].Edited {
		t.Fatalf("new edit text missing from search: total=%d events=%#v", total, events)
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

// ASCII 连续段整体存成一个 token，如果查询不做前缀匹配，搜「diana」就命中不了
// 「dianabot」——而原来的 LIKE 路径是能命中的。这是换索引时最容易悄悄丢掉的一
// 类召回，拿真实语料做过差分才发现，所以钉死。
func TestMessageHistoryFTSKeepsASCIIPrefixRecall(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	session := "onebot-main:group:one"
	for _, event := range []assistant.MessageEvent{
		historySearchEvent(30, "one", "prefix", "Alice", "我在 github 上开了个仓库叫 dianabot"),
		historySearchEvent(20, "one", "stem", "Bob", "deployment 明天再说"),
		historySearchEvent(10, "one", "noise", "Carol", "今天讨论别的菜"),
	} {
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ term, want string }{
		{"diana", "prefix"},
		{"dianabot", "prefix"},
		{"deploy", "stem"},
		{"github", "prefix"},
	} {
		events, _, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
			Session: session, Text: tc.term, FromTime: 0, ThroughTime: 100, Limit: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].MessageID != tc.want {
			t.Fatalf("查 %q 应当只命中 %s，实际命中 %d 条", tc.term, tc.want, len(events))
		}
	}
}

// 引用标记和 CQ 码不进索引：它们切出来的「diana」「reply」「multimedia」是超高频词，
// 搜一个 diana 就要回表十几万行。@ 段留号码，正文照常能搜到。
func TestMessageHistoryFTSSkipsControlMarkers(t *testing.T) {
	text := "[diana-reply:12345][CQ:at,qq=10086] 转发合并修好了 [CQ:image,file=a.png,url=https://multimedia.nt.qq.com.cn/download?appid=1407&amp;fileid=Eh]"
	got := messageHistoryRowTokens("Alice", "10010", text, "")
	for _, noise := range []string{"diana", "reply", "12345", "cq", "multimedia", "https", "appid"} {
		for _, token := range strings.Fields(got) {
			if token == noise {
				t.Fatalf("index tokens kept %q: %s", noise, got)
			}
		}
	}
	for _, keep := range []string{"alice", "10010", "10086", "转发", "修好"} {
		if !strings.Contains(" "+got+" ", " "+keep+" ") {
			t.Fatalf("index tokens lost %q: %s", keep, got)
		}
	}
}

// 存量索引按旧规则切过，要在后台按批重建；做完记下标记，下次启动不再跑。
func TestMessageHistoryFTSCleanupRebuildsLegacyRows(t *testing.T) {
	ctx := context.Background()
	store := ftsStore(t)
	session := "onebot-main:group:one"
	for index, text := range []string{"[diana-reply:12345]转发合并修好了", "diana reply 这几个字本来就写在正文里", "普通消息"} {
		event := historySearchEvent(int64(10+index), "one", fmt.Sprintf("m%d", index), "Alice", text)
		if err := store.AppendMessageEvent(ctx, session, event); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟升级前的索引：按旧规则把整段正文原样切词。
	rows, err := store.db.Query(`SELECT rowid, COALESCE(sender_name, ''), COALESCE(user_id, ''), COALESCE(text, ''), COALESCE(search_extra, '') FROM message_events`)
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[int64]string{}
	for rows.Next() {
		var rowID int64
		var sender, userID, text, extra string
		if err := rows.Scan(&rowID, &sender, &userID, &text, &extra); err != nil {
			t.Fatal(err)
		}
		legacy[rowID] = messageHistoryIndexTokens(sender, userID, text, extra)
	}
	_ = rows.Close()
	for rowID, tokens := range legacy {
		if _, err := store.db.Exec(`UPDATE `+messageHistoryFTSTable+` SET search_text = ? WHERE rowid = ?`, tokens, rowID); err != nil {
			t.Fatal(err)
		}
	}
	countDiana := func() int {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + messageHistoryFTSTable + ` WHERE ` + messageHistoryFTSTable + ` MATCH 'diana'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if countDiana() != 2 {
		t.Fatal("legacy index should match the reply marker")
	}
	if !store.messageHistoryFTSCleanupPending(ctx) {
		t.Fatal("cleanup should be pending before it ran")
	}

	var cursor int64
	for range 5 {
		next, done, err := store.cleanupMessageHistoryFTSBatch(ctx, cursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = next
		if done {
			break
		}
	}
	// 只剩正文里真写着 diana 的那条；引用标记那条不再命中，内容照样能搜到。
	if got := countDiana(); got != 1 {
		t.Fatalf("after cleanup diana matches %d rows, want 1", got)
	}
	events, _, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{Session: session, Text: "转发合并"})
	if err != nil || len(events) != 1 {
		t.Fatalf("content search after cleanup = %d events, err %v", len(events), err)
	}
	if store.messageHistoryFTSCleanupPending(ctx) {
		t.Fatal("cleanup should be marked done")
	}
}

// 按相关度检索时，命中量超过上限的常见词不参与召回：它们几乎不贡献 BM25 分，却要
// 逐行回表。全是常见词、或按时间排序时照常检索。
func TestMessageHistoryRelevanceSearchPrunesCommonTerms(t *testing.T) {
	previous := messageHistoryCommonTermLimit
	messageHistoryCommonTermLimit = 2
	t.Cleanup(func() { messageHistoryCommonTermLimit = previous })
	ctx := context.Background()
	store := ftsStore(t)
	session := "onebot-main:group:one"
	for index, text := range []string{"diana hello", "diana world", "diana again", "chromium only"} {
		if err := store.AppendMessageEvent(ctx, session, historySearchEvent(int64(10+index), "one", fmt.Sprintf("m%d", index), "Alice", text)); err != nil {
			t.Fatal(err)
		}
	}
	search := func(text, order string) int {
		t.Helper()
		events, _, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{Session: session, Text: text, Sort: order})
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	if got := search("diana chromium", "relevance"); got != 1 {
		t.Fatalf("relevance search kept the common term: %d results, want 1", got)
	}
	if got := search("diana", "relevance"); got != 3 {
		t.Fatalf("all-common query must still search: %d results, want 3", got)
	}
	if got := search("diana chromium", "newest"); got != 4 {
		t.Fatalf("chronological search must not prune: %d results, want 4", got)
	}
}
