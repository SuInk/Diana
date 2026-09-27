// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 按消息 ID 找入站事件的几条语句都跑在唯一的写连接上：发送前的并入检查、每条
// 事件的审计回写、追发合并、不知道事件 ID 时的投递回执和连发交接。以前
// inbound_events 没有 message_id 索引，它们全是整表扫描，大库上一次几百毫秒到
// 几秒，写连接上排在后面的入队、领取、记忆任务一起超时。这里钉住它们都走索引。
func TestInboundMessageLookupsUseMessageIndex(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	lookupArgs := []any{"m1", "group", "g1", "u1"}
	handoffWhere, handoffArgs := inboundHandoffRow(assistant.InboundHandoffRef{Event: assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1",
	}})
	deliveryArgs := append(inboundDeliverySetArgs(assistant.OutboundDeliveryGenerated, "", "", 1), lookupArgs...)
	for name, plan := range map[string][]string{
		// InboundEventSuperseded、RecordInboundEventAudit、RecordInboundEventReplyMerge
		// 的定位子查询都是这个形状。
		"latest by message": explainQueryPlan(t, store, `
SELECT id FROM inbound_events
WHERE message_id = ? AND kind = ?
  AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
ORDER BY created_at DESC, id DESC LIMIT 1`, lookupArgs...),
		"handoff fallback":  explainQueryPlan(t, store, `SELECT 1 FROM inbound_events WHERE `+handoffWhere, handoffArgs...),
		"delivery fallback": explainQueryPlan(t, store, inboundDeliveryByMessageSQL, deliveryArgs...),
	} {
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, "idx_inbound_events_message") {
			t.Fatalf("%s does not use idx_inbound_events_message:\n%s", name, joined)
		}
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN inbound_events") {
				t.Fatalf("%s scans inbound_events:\n%s", name, joined)
			}
		}
	}
}

// 按消息 ID 定位的几条写入换了索引之后，结果必须还是「同一消息里最新的那一行」。
func TestInboundMessageLookupsStillPickLatestRow(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().UnixNano()
	insert := func(id, groupID string, createdAt int64) {
		t.Helper()
		if _, err := store.db.Exec(`
INSERT INTO inbound_events (id, session, kind, group_id, user_id, message_id, event_time, payload, status, available_at, created_at, updated_at)
VALUES (?, ?, 'group', ?, 'u1', 'm1', 1, '{}', 'done', 0, ?, ?)`, id, "group:"+groupID, groupID, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	insert("old", "g1", now)
	insert("new", "g1", now+1)
	insert("other-group", "g2", now+2)

	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	if err := store.RecordInboundEventReplyMerge(ctx, event, "root-turn"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{Kind: event.Kind, GroupID: "g1", UserID: "u1", MessageID: "m1", Decision: "reply"}); err != nil {
		t.Fatal(err)
	}
	turnID, superseded, err := store.InboundEventSuperseded(ctx, event)
	if err != nil || !superseded || turnID != "root-turn" {
		t.Fatalf("InboundEventSuperseded = %q, %v, %v", turnID, superseded, err)
	}
	rows, err := store.db.Query(`SELECT id, COALESCE(superseded_by, ''), COALESCE(decision, '') FROM inbound_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, supersededBy, decision string
		if err := rows.Scan(&id, &supersededBy, &decision); err != nil {
			t.Fatal(err)
		}
		got[id] = supersededBy + "|" + decision
	}
	want := map[string]string{"old": "|", "new": "root-turn|reply", "other-group": "|"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// 控制台基线挪到后台读之后，快照必须在 Begin 时就定住：之后才完成的入站事件、
// 才写进来的消息都不能算进基线，否则会和实时计数重复。结果还得和同步读的一致。
func TestDashboardEventSnapshotIsPinnedAtBegin(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dashboard-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if store.readDB == nil {
		t.Fatal("file database should have a read pool")
	}
	now := time.Now()
	insertDone := func(id string) {
		t.Helper()
		at := now.UnixNano()
		if _, err := store.db.Exec(`
INSERT INTO inbound_events (id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload, status, outcome, available_at, created_at, updated_at, completed_at)
VALUES (?, 'group:g', 'group', 'bot-a', 'g', 'u', ?, ?, '{}', 'done', 'replied', 0, ?, ?, ?)`, id, id, now.Unix(), at, at, at+int64(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	insertDone("before")
	want, err := store.DashboardEventStatsSnapshotByProfile(ctx, now)
	if err != nil {
		t.Fatal(err)
	}

	load, err := store.BeginDashboardEventStatsSnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	insertDone("after")
	if _, err := store.db.Exec(`
INSERT INTO message_events (id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload, created_at)
VALUES ('group:g:history', 'group:g', 'group', 'bot-a', 'g', 'u', 'history', ?, '{}', '')`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	got, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if got[""].TotalEvents != 1 || got["bot-a"].HandledEvents != 1 {
		t.Fatalf("pinned baseline saw later writes: %+v", got)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pinned baseline = %+v, want %+v", got, want)
	}
	// 读事务结束后连接要还回去，之后的读能看到新数据。
	after, err := store.DashboardEventStatsSnapshotByProfile(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if after[""].TotalEvents != 3 {
		t.Fatalf("baseline after release = %+v", after[""])
	}
}

// FTS 检索的命中集合必须物化。不物化时规划器会把 CTE 展开进连接，同会话检索先
// 按会话索引扫这个群的每一行，再逐行用 rowid=? 重跑 MATCH 和 BM25——几万条消息的
// 群一次检索要十几秒。物化后全文检索只跑一遍，而且必须挂在 MATERIALIZE 节点下面。
func TestMessageHistoryFTSHitsAreMaterialized(t *testing.T) {
	store := ftsStore(t)
	where := prefixMessageHistoryColumns(`kind != ? AND event_time BETWEEN ? AND ? AND session = ?`)
	for _, order := range []string{"", "newest", "oldest"} {
		pageSQL, countSQL := messageHistoryFTSStatements(where, order, 0)
		args := []any{`"凤爪"*`, "notice", 0, 100, "onebot-main:group:one"}
		for name, query := range map[string]struct {
			sql  string
			args []any
		}{
			"page":  {pageSQL, append(append([]any{}, args...), 10, 0)},
			"count": {countSQL, args},
		} {
			rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			materialize := -1
			ftsParent := -2
			var lines []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, detail)
				if strings.HasPrefix(detail, "MATERIALIZE hits") {
					materialize = id
				}
				if strings.Contains(detail, messageHistoryFTSTable+" VIRTUAL TABLE") {
					ftsParent = parent
				}
			}
			_ = rows.Close()
			if materialize < 0 || ftsParent != materialize {
				t.Fatalf("order=%q %s: MATCH is not evaluated once inside MATERIALIZE hits:\n%s", order, name, strings.Join(lines, "\n"))
			}
		}
	}
}

// 启动补索引改成先只取 rowid 找缺口、再分批回表。缺的行跨好几批时，每一行都得补
// 进去，而且补进去的 token 和写入时同步建的一样，能被检索到。
func TestMessageHistoryFTSBackfillsMissingRowsInChunks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fts-chunks.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := "onebot-main:group:one"
	indexed := historySearchEvent(10, "one", "indexed", "Alice", "已经进了索引的消息")
	if err := store.AppendMessageEvent(ctx, session, indexed); err != nil {
		t.Fatal(err)
	}
	// 直接写正表、不碰索引，模拟入队路径写进来但还没被处理的消息。
	missing := messageHistoryFTSBackfillChunk*2 + 7
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < missing; index++ {
		text := fmt.Sprintf("普通消息%d", index)
		if index == missing-1 {
			text = "最后一批里的虎皮凤爪"
		}
		if _, err := tx.Exec(`
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, 'group', 'one', 'Bob', ?, 'Bob', ?, ?, ?, ?)`,
			fmt.Sprintf("%s:raw-%d", session, index), session, fmt.Sprintf("raw-%d", index), 20+index, text,
			fmt.Sprintf(`{"kind":"group","group_id":"one","user_id":"Bob","message_id":"raw-%d","sender_name":"Bob","time":%d,"raw_message":%q}`, index, 20+index, text),
			time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var ftsRows, eventRows int
	if err := reopened.db.QueryRow(`SELECT (SELECT COUNT(*) FROM `+messageHistoryFTSTable+`), (SELECT COUNT(*) FROM message_events)`).Scan(&ftsRows, &eventRows); err != nil {
		t.Fatal(err)
	}
	if ftsRows != eventRows || eventRows != missing+1 {
		t.Fatalf("indexed %d of %d rows, want %d", ftsRows, eventRows, missing+1)
	}
	var tokens string
	if err := reopened.db.QueryRow(`SELECT f.search_text FROM `+messageHistoryFTSTable+` AS f JOIN message_events AS e ON e.rowid = f.rowid WHERE e.id = ?`,
		session+":raw-0").Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if want := messageHistoryIndexTokens("Bob", "Bob", "普通消息0", ""); tokens != want {
		t.Fatalf("backfilled tokens = %q, want %q", tokens, want)
	}
	found, total, err := reopened.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{Session: session, Text: "虎皮凤爪", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(found) != 1 || found[0].MessageID != fmt.Sprintf("raw-%d", missing-1) {
		t.Fatalf("search after backfill = %d %+v", total, found)
	}
}

// 事件列表给 @ 补昵称时，每个号取最近一条带名片的消息，空名片的消息跳过，
// 查不到的号不出现在结果里。
func TestResolveMentionNamesPicksLatestNonEmptyCard(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	insert := func(id, userID, sender string, at int64) {
		t.Helper()
		if _, err := store.db.Exec(`
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, 'group:g', 'group', 'g', ?, ?, ?, ?, '', '{}', '')`, id, userID, id, sender, at); err != nil {
			t.Fatal(err)
		}
	}
	insert("a1", "alice", "旧名片", 10)
	insert("a2", "alice", "新名片", 20)
	insert("a3", "alice", "  ", 30)
	insert("b1", "bob", "", 10)
	names, err := store.resolveMentionNames(ctx, map[string]struct{}{"alice": {}, "bob": {}, "carol": {}})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(names) != fmt.Sprint(map[string]string{"alice": "新名片"}) {
		t.Fatalf("names = %v", names)
	}
}
